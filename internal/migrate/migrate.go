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
