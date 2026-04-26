// Package store — history event-log operations.
//
// AppendEvent, QueryEvents, and CleanupHistory provide the persistence layer
// for the port-event timeline (design §4.9, §7.1, §7.6).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// EventType represents the kind of history event.
type EventType string

const (
	EventOccupied   EventType = "occupied"
	EventReleased   EventType = "released"
	EventReserved   EventType = "reserved"
	EventUnreserved EventType = "unreserved"
)

// validEventTypes holds the set of accepted EventType values.
var validEventTypes = map[EventType]bool{
	EventOccupied:   true,
	EventReleased:   true,
	EventReserved:   true,
	EventUnreserved: true,
}

// Event is a single row from the history table.
type Event struct {
	ID        int64
	Port      int
	Event     EventType
	Occupant  string    // empty when SQL NULL
	Timestamp time.Time // UTC
}

// HistoryQuery carries optional filter parameters for QueryEvents.
type HistoryQuery struct {
	Port  *int      // nil = no filter
	Event EventType // empty = no filter
	Since time.Time // zero = no filter (otherwise: timestamp >= Since)
	Limit int       // 0 = no limit
}

// sqliteTimestampFormats lists the layouts SQLite's CURRENT_TIMESTAMP may use.
// The standard text format from SQLite is "2006-01-02 15:04:05".
var sqliteTimestampFormats = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
	time.RFC3339,
}

// parseUTCTimestamp parses a timestamp string stored by SQLite, treating it as
// UTC regardless of whether a timezone indicator is present.
func parseUTCTimestamp(s string) (time.Time, error) {
	for _, layout := range sqliteTimestampFormats {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("history: cannot parse timestamp %q", s)
}

// AppendEvent writes a new event row to the history table. If e.Timestamp is
// the zero value, SQLite's DEFAULT CURRENT_TIMESTAMP is used; otherwise the
// caller's timestamp is stored in UTC. An empty e.Occupant is stored as NULL.
// Returns the inserted event with ID and Timestamp filled in.
func (s *Store) AppendEvent(ctx context.Context, e Event) (Event, error) {
	if !validEventTypes[e.Event] {
		return Event{}, fmt.Errorf("invalid event type: %q", e.Event)
	}

	var occupant sql.NullString
	if e.Occupant != "" {
		occupant = sql.NullString{String: e.Occupant, Valid: true}
	}

	var result Event

	if e.Timestamp.IsZero() {
		// Let SQLite set the timestamp via DEFAULT CURRENT_TIMESTAMP.
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO history (port, event, occupant) VALUES (?, ?, ?)`,
			e.Port, string(e.Event), occupant,
		)
		if err != nil {
			return Event{}, fmt.Errorf("AppendEvent: insert: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return Event{}, fmt.Errorf("AppendEvent: last insert id: %w", err)
		}

		// Read back the stored row to get the DB-assigned timestamp.
		var tsStr string
		var occ sql.NullString
		err = s.db.QueryRowContext(ctx,
			`SELECT port, event, occupant, timestamp FROM history WHERE id = ?`, id,
		).Scan(&result.Port, (*string)(&result.Event), &occ, &tsStr)
		if err != nil {
			return Event{}, fmt.Errorf("AppendEvent: read back: %w", err)
		}
		ts, err := parseUTCTimestamp(tsStr)
		if err != nil {
			return Event{}, err
		}
		result.ID = id
		result.Timestamp = ts
		if occ.Valid {
			result.Occupant = occ.String
		}
	} else {
		// Caller supplies the timestamp — store it as UTC text.
		tsStr := e.Timestamp.UTC().Format("2006-01-02 15:04:05")
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO history (port, event, occupant, timestamp) VALUES (?, ?, ?, ?)`,
			e.Port, string(e.Event), occupant, tsStr,
		)
		if err != nil {
			return Event{}, fmt.Errorf("AppendEvent: insert with timestamp: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return Event{}, fmt.Errorf("AppendEvent: last insert id: %w", err)
		}
		result = Event{
			ID:        id,
			Port:      e.Port,
			Event:     e.Event,
			Timestamp: e.Timestamp.UTC(),
		}
		if occupant.Valid {
			result.Occupant = occupant.String
		}
	}

	return result, nil
}

// QueryEvents returns history events matching q, ordered by timestamp DESC,
// id DESC (newest first; id as tiebreaker for same-tick events).
func (s *Store) QueryEvents(ctx context.Context, q HistoryQuery) ([]Event, error) {
	var (
		sb   strings.Builder
		args []any
	)

	sb.WriteString(`SELECT id, port, event, occupant, timestamp FROM history`)

	var conditions []string
	if q.Port != nil {
		conditions = append(conditions, `port = ?`)
		args = append(args, *q.Port)
	}
	if q.Event != "" {
		conditions = append(conditions, `event = ?`)
		args = append(args, string(q.Event))
	}
	if !q.Since.IsZero() {
		conditions = append(conditions, `timestamp >= ?`)
		args = append(args, q.Since.UTC().Format("2006-01-02 15:04:05"))
	}

	if len(conditions) > 0 {
		sb.WriteString(` WHERE `)
		sb.WriteString(strings.Join(conditions, ` AND `))
	}

	sb.WriteString(` ORDER BY timestamp DESC, id DESC`)

	if q.Limit > 0 {
		sb.WriteString(` LIMIT ?`)
		args = append(args, q.Limit)
	}

	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("QueryEvents: query: %w", err)
	}
	defer rows.Close()

	// Initialise to non-nil empty slice so callers get [] not nil.
	events := make([]Event, 0)

	for rows.Next() {
		var (
			e     Event
			occ   sql.NullString
			tsStr string
		)
		if err := rows.Scan(&e.ID, &e.Port, (*string)(&e.Event), &occ, &tsStr); err != nil {
			return nil, fmt.Errorf("QueryEvents: scan: %w", err)
		}
		ts, err := parseUTCTimestamp(tsStr)
		if err != nil {
			return nil, err
		}
		e.Timestamp = ts
		if occ.Valid {
			e.Occupant = occ.String
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("QueryEvents: rows: %w", err)
	}

	return events, nil
}

// CleanupHistory deletes rows where timestamp < cutoff (strict less-than).
// Returns the number of rows deleted. Used by the daily cleanup goroutine
// (design §7.6) with cutoff = time.Now().UTC().AddDate(0, 0, -30).
func (s *Store) CleanupHistory(ctx context.Context, cutoff time.Time) (int64, error) {
	cutoffStr := cutoff.UTC().Format("2006-01-02 15:04:05")
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM history WHERE timestamp < ?`, cutoffStr,
	)
	if err != nil {
		return 0, fmt.Errorf("CleanupHistory: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("CleanupHistory: rows affected: %w", err)
	}
	return n, nil
}

