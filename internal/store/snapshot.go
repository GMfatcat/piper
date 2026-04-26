// Package store — scan snapshot persistence layer.
//
// SaveSnapshot atomically replaces the entire scan_snapshots table and updates
// the single scan_meta row, all within one SQLite transaction.
// GetSnapshot reads back the current snapshot rows plus the last_scan_at time.
//
// NULL handling contract:
//   - On write: zero int → SQL NULL, empty string → SQL NULL.
//   - On read:  SQL NULL int → 0, SQL NULL string → "".
//
// All timestamps are stored and returned as UTC (design §13.5).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SnapshotState represents the classification of a scanned port.
type SnapshotState string

const (
	StateUsedProcess      SnapshotState = "used_process"
	StateUsedDocker       SnapshotState = "used_docker"
	StateReservedImplicit SnapshotState = "reserved_implicit"
	StateFree             SnapshotState = "free" // currently never persisted; reserved for future
)

// validSnapshotStates is the complete set of accepted SnapshotState values.
var validSnapshotStates = map[SnapshotState]bool{
	StateUsedProcess:      true,
	StateUsedDocker:       true,
	StateReservedImplicit: true,
	StateFree:             true,
}

// SnapshotRow is one row from the scan_snapshots table.
//
// Zero-value int fields (PID, UFWRuleNum) map to SQL NULL.
// Empty string fields map to SQL NULL.
type SnapshotRow struct {
	Port            int
	State           SnapshotState
	PID             int    // 0 = NULL
	ProcessName     string // "" = NULL
	Cmdline         string // "" = NULL
	ContainerID     string // "" = NULL
	ContainerName   string // "" = NULL
	ContainerImage  string // "" = NULL
	ContainerStatus string // "" = NULL
	UFWAction       string // "" = NULL (one of: "allow", "deny", "limit", "complex")
	UFWRuleNum      int    // 0 = NULL
}

// SaveSnapshot atomically replaces the entire snapshot:
//
//	BEGIN; DELETE FROM scan_snapshots; INSERT each row;
//	UPSERT scan_meta(1, scannedAt); COMMIT;
//
// Validates each row's State is one of the four constants before opening the
// transaction; returns error on bad input without writing anything.
//
// If scannedAt.IsZero(), time.Now().UTC() is used instead.
// An empty rows slice is valid — it still updates scan_meta.
func (s *Store) SaveSnapshot(ctx context.Context, rows []SnapshotRow, scannedAt time.Time) error {
	// --- Validation pass (BEFORE opening the transaction) ---
	for i, r := range rows {
		if !validSnapshotStates[r.State] {
			return fmt.Errorf("SaveSnapshot: rows[%d].State %q is not a valid SnapshotState", i, r.State)
		}
	}

	// Resolve zero scannedAt.
	if scannedAt.IsZero() {
		scannedAt = time.Now().UTC()
	}
	scannedAt = scannedAt.UTC()

	// --- Transaction ---
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("SaveSnapshot: begin tx: %w", err)
	}
	// Defer rollback — no-op if Commit() has already been called.
	defer tx.Rollback() //nolint:errcheck

	// Step 1: Clear previous snapshot.
	if _, err := tx.ExecContext(ctx, `DELETE FROM scan_snapshots`); err != nil {
		return fmt.Errorf("SaveSnapshot: delete scan_snapshots: %w", err)
	}

	// Step 2: Insert each row.
	const insertSQL = `
INSERT INTO scan_snapshots
    (port, state, pid, process_name, cmdline,
     container_id, container_name, container_image, container_status,
     ufw_action, ufw_rule_num)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, insertSQL,
			r.Port,
			string(r.State),
			nullInt(r.PID),
			nullString(r.ProcessName),
			nullString(r.Cmdline),
			nullString(r.ContainerID),
			nullString(r.ContainerName),
			nullString(r.ContainerImage),
			nullString(r.ContainerStatus),
			nullString(r.UFWAction),
			nullInt(r.UFWRuleNum),
		); err != nil {
			return fmt.Errorf("SaveSnapshot: insert port %d: %w", r.Port, err)
		}
	}

	// Step 3: Upsert scan_meta (only ever one row, id=1).
	tsStr := scannedAt.Format("2006-01-02T15:04:05.999999999Z")
	const upsertMeta = `
INSERT INTO scan_meta (id, last_scan_at) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET last_scan_at = excluded.last_scan_at`
	if _, err := tx.ExecContext(ctx, upsertMeta, tsStr); err != nil {
		return fmt.Errorf("SaveSnapshot: upsert scan_meta: %w", err)
	}

	// Commit.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("SaveSnapshot: commit: %w", err)
	}
	return nil
}

// GetSnapshot returns all rows from scan_snapshots ordered by port ASC, plus
// the last_scan_at timestamp from scan_meta.
//
// If scan_meta has no row yet (SaveSnapshot has never run), returns
// empty slice + zero time.Time + nil error — the caller decides what to do.
func (s *Store) GetSnapshot(ctx context.Context) ([]SnapshotRow, time.Time, error) {
	// --- Read scan_meta ---
	var lastScanAt time.Time
	var tsStr string
	err := s.db.QueryRowContext(ctx, `SELECT last_scan_at FROM scan_meta WHERE id = 1`).Scan(&tsStr)
	switch {
	case err == nil:
		lastScanAt, err = parseTimestamp(tsStr)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("GetSnapshot: parse last_scan_at %q: %w", tsStr, err)
		}
	case err == sql.ErrNoRows:
		// No SaveSnapshot has ever run — this is not an error.
		lastScanAt = time.Time{}
	default:
		return nil, time.Time{}, fmt.Errorf("GetSnapshot: query scan_meta: %w", err)
	}

	// --- Read scan_snapshots ---
	const selectSQL = `
SELECT port, state, pid, process_name, cmdline,
       container_id, container_name, container_image, container_status,
       ufw_action, ufw_rule_num
FROM scan_snapshots
ORDER BY port ASC`

	rows, err := s.db.QueryContext(ctx, selectSQL)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("GetSnapshot: query scan_snapshots: %w", err)
	}
	defer rows.Close()

	result := make([]SnapshotRow, 0)

	for rows.Next() {
		var r SnapshotRow
		var (
			pid             sql.NullInt64
			processName     sql.NullString
			cmdline         sql.NullString
			containerID     sql.NullString
			containerName   sql.NullString
			containerImage  sql.NullString
			containerStatus sql.NullString
			ufwAction       sql.NullString
			ufwRuleNum      sql.NullInt64
		)

		if err := rows.Scan(
			&r.Port,
			(*string)(&r.State),
			&pid,
			&processName,
			&cmdline,
			&containerID,
			&containerName,
			&containerImage,
			&containerStatus,
			&ufwAction,
			&ufwRuleNum,
		); err != nil {
			return nil, time.Time{}, fmt.Errorf("GetSnapshot: scan row: %w", err)
		}

		// Map SQL NULLs back to zero values.
		if pid.Valid {
			r.PID = int(pid.Int64)
		}
		if processName.Valid {
			r.ProcessName = processName.String
		}
		if cmdline.Valid {
			r.Cmdline = cmdline.String
		}
		if containerID.Valid {
			r.ContainerID = containerID.String
		}
		if containerName.Valid {
			r.ContainerName = containerName.String
		}
		if containerImage.Valid {
			r.ContainerImage = containerImage.String
		}
		if containerStatus.Valid {
			r.ContainerStatus = containerStatus.String
		}
		if ufwAction.Valid {
			r.UFWAction = ufwAction.String
		}
		if ufwRuleNum.Valid {
			r.UFWRuleNum = int(ufwRuleNum.Int64)
		}

		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, fmt.Errorf("GetSnapshot: rows: %w", err)
	}

	return result, lastScanAt, nil
}

// nullInt converts a zero int to nil (SQL NULL) and a non-zero int to the
// value itself. SQLite will store nil as NULL.
func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// nullString converts an empty string to nil (SQL NULL) and a non-empty string
// to the value itself.
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
