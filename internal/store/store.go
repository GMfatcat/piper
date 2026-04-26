// Package store provides SQLite persistence for Piper port management.
// It manages the database connection lifecycle and schema migrations.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	// modernc.org/sqlite is a pure-Go SQLite driver (no cgo), required for
	// cross-compilation to aarch64 (DGX Spark). Do NOT replace with mattn/go-sqlite3.
	_ "modernc.org/sqlite"
)

// Store holds the active SQLite database connection.
type Store struct {
	db *sql.DB
}

// Open connects to (or creates) a SQLite database at path, ensures the parent
// directory exists with mode 0700, ensures the DB file is mode 0600 after
// creation, applies all pending migrations idempotently, and returns a ready
// Store. Safe to call repeatedly on the same path.
func Open(path string) (*Store, error) {
	// 1. Ensure parent directory exists with mode 0700.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("store.Open: create parent dir %q: %w", dir, err)
	}

	// 2. Open (or create) the SQLite database.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store.Open: sql.Open %q: %w", path, err)
	}

	// Ping to actually trigger file creation and verify connectivity.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store.Open: ping %q: %w", path, err)
	}

	// 3. Restrict file permissions to 0600 (owner read/write only).
	//    On Windows chmod is a no-op for bits beyond read-only, which is
	//    acceptable — the design doc calls this out in §11.4.
	if err := os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, fmt.Errorf("store.Open: chmod %q: %w", path, err)
	}

	s := &Store{db: db}

	// 4. Apply pending migrations.
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store.Open: migrate: %w", err)
	}

	return s, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the raw *sql.DB for sibling files (snapshot.go, history.go, etc.)
// within this package. This is an internal helper, not part of the public API
// surface.
func (s *Store) DB() *sql.DB {
	return s.db
}
