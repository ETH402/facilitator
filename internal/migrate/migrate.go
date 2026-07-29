package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

const lockID int64 = 402

type VersionQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// CheckApplied fails when the database and binary do not carry the exact same
// migration set. Missing migrations mean the application would query a schema
// that does not exist; unknown newer migrations mean an older binary is being
// rolled onto a schema it was never tested against.
func CheckApplied(ctx context.Context, database VersionQueryer, files fs.FS) error {
	names, err := migrationNames(files, ".up.sql")
	if err != nil {
		return err
	}
	expected := make([]string, len(names))
	for i, name := range names {
		expected[i] = strings.TrimSuffix(name, ".up.sql")
	}
	rows, err := database.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("read schema migrations: %w", err)
	}
	defer rows.Close()
	var applied []string
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return err
		}
		applied = append(applied, version)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(applied) != len(expected) {
		return fmt.Errorf("schema migration mismatch: database has %d versions, binary requires %d",
			len(applied), len(expected))
	}
	for i := range expected {
		if applied[i] != expected[i] {
			return fmt.Errorf("schema migration mismatch at version %d: database has %q, binary requires %q",
				i+1, applied[i], expected[i])
		}
	}
	return nil
}

func Up(ctx context.Context, conn *pgx.Conn, files fs.FS) error {
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}
	return withLock(ctx, conn, func() error {
		names, err := migrationNames(files, ".up.sql")
		if err != nil {
			return err
		}
		for _, name := range names {
			version := strings.TrimSuffix(name, ".up.sql")
			var exists bool
			if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)", version).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			sql, err := fs.ReadFile(files, name)
			if err != nil {
				return err
			}
			tx, err := conn.Begin(ctx)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, string(sql)); err == nil {
				_, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES ($1)", version)
			}
			if err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("apply migration %s: %w", version, err)
			}
			if err := tx.Commit(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

func Down(ctx context.Context, conn *pgx.Conn, files fs.FS) error {
	return withLock(ctx, conn, func() error {
		var version string
		err := conn.QueryRow(ctx, "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1").Scan(&version)
		if err != nil {
			return err
		}
		name := version + ".down.sql"
		sql, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(sql)); err == nil {
			_, err = tx.Exec(ctx, "DELETE FROM schema_migrations WHERE version=$1", version)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("revert migration %s: %w", version, err)
		}
		return tx.Commit(ctx)
	})
}

func migrationNames(files fs.FS, suffix string) ([]string, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func withLock(ctx context.Context, conn *pgx.Conn, fn func() error) error {
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID)
	}()
	return fn()
}
