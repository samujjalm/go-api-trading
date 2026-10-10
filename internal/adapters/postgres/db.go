// Package postgres implements the app.Store port on top of PostgreSQL using
// pgx.
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/samujjal/go-service/internal/domain"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Arbitrary constant so concurrently starting instances don't race to migrate.
const migrationLockID = 727_001

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrate applies embedded migrations not yet recorded in schema_migrations,
// in filename order, each in its own transaction. Production teams usually
// reach for a tool (goose, golang-migrate, atlas); this keeps the moving
// parts visible.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockID)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, name := range files {
		var applied bool
		if err := conn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", name,
		).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		sql, err := migrationsFS.ReadFile(name)
		if err != nil {
			return err
		}
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name)
			return err
		}); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}

// mapErr translates driver errors into domain errors so nothing above this
// package ever needs to import pgx.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return domain.ErrConflict
		case "22P02": // invalid_text_representation, e.g. a malformed UUID in the URL
			return domain.ErrNotFound
		}
	}
	return err
}
