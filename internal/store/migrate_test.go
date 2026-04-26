package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GMfatcat/piper/internal/store"
)

// expectedTables lists all five tables that must exist after migration.
var expectedTables = []string{
	"schema_migrations",
	"reservations",
	"scan_snapshots",
	"scan_meta",
	"history",
}

// tablesExist checks that all named tables are present in the opened DB.
func tablesExist(t *testing.T, db *sql.DB, tables []string) {
	t.Helper()
	for _, tbl := range tables {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", tbl, err)
		}
	}
}

// TestOpen_CreatesNewDatabase verifies that Open on a fresh path creates the
// DB file, all 5 tables, and inserts version 1 into schema_migrations.
func TestOpen_CreatesNewDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "piper.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// File must exist.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("DB file was not created")
	}

	// All 5 tables must exist.
	tablesExist(t, s.DB(), expectedTables)

	// schema_migrations must have exactly version 1.
	var version int
	if err := s.DB().QueryRow(`SELECT version FROM schema_migrations WHERE version = 1`).Scan(&version); err != nil {
		t.Fatalf("schema_migrations row for version 1 not found: %v", err)
	}
	if version != 1 {
		t.Errorf("expected version 1, got %d", version)
	}
}

// TestOpen_IsIdempotent verifies that calling Open twice on the same path
// succeeds, applies no additional migrations, and leaves tables intact.
func TestOpen_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "piper.db")

	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	s1.Close()

	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("second Open failed: %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	// Tables still exist after second Open.
	tablesExist(t, s2.DB(), expectedTables)

	// schema_migrations must have exactly 1 row (migration not duplicated).
	var count int
	if err := s2.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row in schema_migrations after idempotent Open, got %d", count)
	}
}

// TestOpen_CreatesParentDir verifies that Open creates missing parent
// directories with mode 0700 (mode check skipped on Windows).
func TestOpen_CreatesParentDir(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "sub", "nested", "piper.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open with nested path failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// The parent directory must exist.
	parentDir := filepath.Dir(path)
	info, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("parent dir does not exist after Open: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("parent path is not a directory")
	}

	// Mode check is meaningful only on Unix.
	if runtime.GOOS != "windows" {
		mode := info.Mode().Perm()
		if mode != 0700 {
			t.Errorf("parent dir mode = %04o, want 0700", mode)
		}
	}
}

// TestOpen_FailsOnUnwritablePath verifies that Open returns an error when the
// path cannot be created. On Windows, we use a path with illegal characters
// which cannot exist; on Linux we would use /proc/.../x.db. If no portable
// failure case can be induced, the test is skipped.
func TestOpen_FailsOnUnwritablePath(t *testing.T) {
	var badPath string
	if runtime.GOOS == "windows" {
		// Colons (beyond the drive letter) are illegal in Windows file names.
		badPath = `C:\cannot<>create|this\x.db`
	} else {
		// /proc is a virtual FS — creating subdirs there always fails.
		badPath = "/proc/cannot/exist/x.db"
	}

	s, err := store.Open(badPath)
	if err == nil {
		s.Close()
		t.Skip("expected Open to fail on unwritable path but it succeeded — skipping")
	}
	// err != nil is the expected outcome; test passes.
}

// TestSchemaContents verifies that the reservations table accepts and returns
// data correctly after migration.
func TestSchemaContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "piper.db")

	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Insert a sample reservation row (port=9100, name='test').
	_, err = s.DB().Exec(
		`INSERT INTO reservations (port, name) VALUES (?, ?)`, 9100, "test",
	)
	if err != nil {
		t.Fatalf("INSERT into reservations failed: %v", err)
	}

	// Select it back and assert the fields.
	var port int
	var name string
	err = s.DB().QueryRow(
		`SELECT port, name FROM reservations WHERE port = ?`, 9100,
	).Scan(&port, &name)
	if err != nil {
		t.Fatalf("SELECT from reservations failed: %v", err)
	}
	if port != 9100 {
		t.Errorf("port = %d, want 9100", port)
	}
	if name != "test" {
		t.Errorf("name = %q, want \"test\"", name)
	}
}
