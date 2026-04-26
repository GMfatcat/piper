// Package store — migration engine.
//
// embed decision: Go's //go:embed directive does not allow paths that escape
// the package directory (e.g. "../../migrations" is forbidden by the spec).
// Per the task design, the canonical SQL source lives at
//   internal/store/migrations/001_init.sql
// A project-root migrations/ directory may be added later as documentation
// or a symlink; for now the embedded files are authoritative.
//
// Migrations are applied in lexicographic filename order (001, 002, ...).
// Each migration runs inside its own transaction. After the SQL executes, a
// row is inserted into schema_migrations with the version number parsed from
// the filename prefix "NNN_". On subsequent Open() calls, already-applied
// versions are read from schema_migrations and skipped.
package store

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrate reads all embedded *.sql files, determines which versions have
// already been applied, and applies the remainder in order.
func (s *Store) migrate() error {
	// 1. Ensure schema_migrations table exists so we can track applied versions.
	//    This bootstrap DDL is idempotent and runs outside a transaction so
	//    that it succeeds even on a brand-new database.
	const bootstrap = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := s.db.Exec(bootstrap); err != nil {
		return fmt.Errorf("migrate: bootstrap schema_migrations: %w", err)
	}

	// 2. Read the set of already-applied versions.
	applied, err := s.appliedVersions()
	if err != nil {
		return err
	}

	// 3. Collect migration files in lexicographic order.
	files, err := migrationFiles()
	if err != nil {
		return err
	}

	// 4. Apply each pending migration in a transaction.
	for _, f := range files {
		version, err := versionFromFilename(f)
		if err != nil {
			return err
		}
		if applied[version] {
			continue // already applied — idempotent
		}
		if err := s.applyMigration(f, version); err != nil {
			return fmt.Errorf("migrate: apply %s (version %d): %w", f, version, err)
		}
	}
	return nil
}

// appliedVersions queries schema_migrations and returns a set of version numbers.
func (s *Store) appliedVersions() (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("migrate: query applied versions: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("migrate: scan version: %w", err)
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// migrationFiles returns the embedded SQL filenames sorted lexicographically.
func migrationFiles() ([]string, error) {
	var names []string
	err := fs.WalkDir(migrationsFS, "migrations", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".sql") {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("migrate: walk embedded migrations: %w", err)
	}
	sort.Strings(names)
	return names, nil
}

// versionFromFilename parses the integer version prefix from a filename like
// "migrations/001_init.sql" → 1.
func versionFromFilename(path string) (int, error) {
	base := filepath.Base(path)
	idx := strings.Index(base, "_")
	if idx < 0 {
		return 0, fmt.Errorf("migrate: filename %q has no underscore separator", base)
	}
	v, err := strconv.Atoi(base[:idx])
	if err != nil {
		return 0, fmt.Errorf("migrate: parse version from %q: %w", base, err)
	}
	return v, nil
}

// applyMigration runs a single SQL file inside a transaction and records the
// version in schema_migrations on success.
func (s *Store) applyMigration(path string, version int) error {
	content, err := migrationsFS.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read embedded file %s: %w", path, err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	// Execute the migration SQL.
	if _, err := tx.Exec(string(content)); err != nil {
		tx.Rollback() //nolint:errcheck
		return fmt.Errorf("exec SQL: %w", err)
	}

	// Record the applied version.
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		tx.Rollback() //nolint:errcheck
		return fmt.Errorf("record version %d: %w", version, err)
	}

	return tx.Commit()
}
