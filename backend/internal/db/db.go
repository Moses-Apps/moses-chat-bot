package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrMissingTenantContext is returned by Store methods when a tenant_id-bearing
// query is called with uuid.Nil. Every relay table is tenant-scoped; passing the
// zero UUID is a programming error that would otherwise cross-tenant-leak.
var ErrMissingTenantContext = errors.New("missing tenant context: tenant_id is zero UUID")

// Open returns a pgxpool with sensible defaults (max 10, min 5, 30s idle, 1h max-lifetime).
// If dsn is empty, DATABASE_URL is read from the environment.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		return nil, fmt.Errorf("no DSN provided and DATABASE_URL is unset")
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse pgx config: %w", err)
	}

	cfg.MaxConns = 10
	cfg.MinConns = 5
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("new pgxpool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	return pool, nil
}

// Close gracefully closes the pool. Safe to call with nil.
func Close(pool *pgxpool.Pool) {
	if pool != nil {
		pool.Close()
	}
}
