// Package store — Reservation CRUD operations.
//
// All timestamp values are stored and read as UTC (design §13.5).
// Nullable fields (Note, CreatedBy) are stored as SQL NULL when empty and
// returned as empty string when NULL.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Reservation represents a port reservation persisted in the reservations table.
type Reservation struct {
	Port      int
	Name      string
	Note      string    // empty string when SQL NULL
	CreatedAt time.Time // UTC
	CreatedBy string    // empty string when SQL NULL
}

// ErrReservationExists is returned by InsertReservation when the port is
// already reserved.
var ErrReservationExists = errors.New("reservation already exists for this port")

// ErrReservationNotFound is returned by GetReservation and DeleteReservation
// when the port has no reservation.
var ErrReservationNotFound = errors.New("reservation not found")

// InsertReservation creates a new reservation row. If r.CreatedAt is the zero
// value, SQLite's DEFAULT CURRENT_TIMESTAMP fills it in. If non-zero, the
// caller-provided UTC timestamp is stored explicitly.
//
// Returns ErrReservationExists if the port already has a reservation.
func (s *Store) InsertReservation(ctx context.Context, r Reservation) error {
	// Convert empty strings to nil so SQLite stores SQL NULL.
	var note, createdBy any
	if r.Note != "" {
		note = r.Note
	}
	if r.CreatedBy != "" {
		createdBy = r.CreatedBy
	}

	var err error
	if r.CreatedAt.IsZero() {
		// Let SQLite default CURRENT_TIMESTAMP.
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO reservations (port, name, note, created_by)
			 VALUES (?, ?, ?, ?)`,
			r.Port, r.Name, note, createdBy,
		)
	} else {
		// Store the caller-provided UTC timestamp as an ISO-8601 string so SQLite
		// can parse it back correctly.
		ts := r.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO reservations (port, name, note, created_at, created_by)
			 VALUES (?, ?, ?, ?, ?)`,
			r.Port, r.Name, note, ts, createdBy,
		)
	}

	if err != nil {
		// modernc.org/sqlite surfaces UNIQUE constraint failures with error code 19
		// (SQLITE_CONSTRAINT). Check for that specifically to return a typed error.
		if isConstraintError(err) {
			return fmt.Errorf("%w: port %d", ErrReservationExists, r.Port)
		}
		return fmt.Errorf("InsertReservation port %d: %w", r.Port, err)
	}
	return nil
}

// GetReservation fetches the reservation for the given port.
// Returns ErrReservationNotFound if no row exists.
func (s *Store) GetReservation(ctx context.Context, port int) (Reservation, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT port, name, note, created_at, created_by
		 FROM reservations
		 WHERE port = ?`,
		port,
	)

	var r Reservation
	var note, createdBy sql.NullString
	var createdAtRaw string

	err := row.Scan(&r.Port, &r.Name, &note, &createdAtRaw, &createdBy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Reservation{}, fmt.Errorf("%w: port %d", ErrReservationNotFound, port)
		}
		return Reservation{}, fmt.Errorf("GetReservation port %d: %w", port, err)
	}

	// Map SQL NULLs back to empty strings.
	if note.Valid {
		r.Note = note.String
	}
	if createdBy.Valid {
		r.CreatedBy = createdBy.String
	}

	// Parse the timestamp stored by SQLite. SQLite CURRENT_TIMESTAMP stores as
	// "YYYY-MM-DD HH:MM:SS" in UTC; explicit values may include sub-seconds.
	r.CreatedAt, err = parseTimestamp(createdAtRaw)
	if err != nil {
		return Reservation{}, fmt.Errorf("GetReservation port %d: parse created_at %q: %w", port, createdAtRaw, err)
	}

	return r, nil
}

// DeleteReservation removes the reservation for the given port.
// Returns ErrReservationNotFound if no row existed.
func (s *Store) DeleteReservation(ctx context.Context, port int) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM reservations WHERE port = ?`,
		port,
	)
	if err != nil {
		return fmt.Errorf("DeleteReservation port %d: %w", port, err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("DeleteReservation port %d: rows affected: %w", port, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: port %d", ErrReservationNotFound, port)
	}
	return nil
}

// ListReservations returns all reservations ordered by port ascending.
// Returns an empty non-nil slice when there are no reservations.
func (s *Store) ListReservations(ctx context.Context) ([]Reservation, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT port, name, note, created_at, created_by
		 FROM reservations
		 ORDER BY port ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("ListReservations: %w", err)
	}
	defer rows.Close()

	// Initialise to a non-nil empty slice so callers get [] not null in JSON.
	result := make([]Reservation, 0)

	for rows.Next() {
		var r Reservation
		var note, createdBy sql.NullString
		var createdAtRaw string

		if err := rows.Scan(&r.Port, &r.Name, &note, &createdAtRaw, &createdBy); err != nil {
			return nil, fmt.Errorf("ListReservations: scan: %w", err)
		}

		if note.Valid {
			r.Note = note.String
		}
		if createdBy.Valid {
			r.CreatedBy = createdBy.String
		}

		r.CreatedAt, err = parseTimestamp(createdAtRaw)
		if err != nil {
			return nil, fmt.Errorf("ListReservations: parse created_at %q: %w", createdAtRaw, err)
		}

		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListReservations: rows: %w", err)
	}
	return result, nil
}

// parseTimestamp handles the various SQLite timestamp formats:
//   - "YYYY-MM-DD HH:MM:SS"           (CURRENT_TIMESTAMP default)
//   - "YYYY-MM-DDThh:mm:ss.nnnnnnnnnZ" (our explicit ISO-8601 format)
//
// All returned times are in UTC.
func parseTimestamp(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02T15:04:05.999999999Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp format %q", s)
}

// isConstraintError reports whether err is a SQLite UNIQUE / PRIMARY KEY
// constraint violation. modernc.org/sqlite wraps the error; we check the
// message text as a reliable fallback.
func isConstraintError(err error) bool {
	if err == nil {
		return false
	}
	// The modernc driver exposes error codes via an interface; use message
	// heuristics which is portable and avoids importing driver internals.
	msg := err.Error()
	return contains(msg, "UNIQUE constraint") || contains(msg, "PRIMARY KEY constraint")
}

// contains is a simple substring check (avoids importing strings in a way that
// adds a dependency; strings is in stdlib so this is just clarity).
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
