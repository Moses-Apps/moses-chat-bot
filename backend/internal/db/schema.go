package db

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"moses-chat-bot/backend/postgresql"
)

const migrationsTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    filename   VARCHAR(255) PRIMARY KEY,
    checksum   CHAR(64),
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status     VARCHAR(32) NOT NULL DEFAULT 'applied'
);
`

// ApplySchema applies any embedded SQL schema files that are not yet recorded in
// schema_migrations, in lexicographic order. Each file runs inside its own
// transaction; the row in schema_migrations is inserted in the same transaction
// so a partial DDL apply never leaves the tracker in an inconsistent state.
//
// Forward-only: already-applied filenames are skipped without re-checking the
// checksum (matches the moses-platform-prep policy — checksum is recorded for
// drift detection by future tooling, not enforced on read).
//
// Halt-on-error: returns on the first failed file. Caller decides whether to
// log+exit or surface to the operator.
func ApplySchema(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("nil pool")
	}

	if _, err := pool.Exec(ctx, migrationsTableDDL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	schemaSource, err := loadSchemaSource()
	if err != nil {
		return err
	}

	applied, err := loadAppliedFilenames(ctx, pool)
	if err != nil {
		return fmt.Errorf("load applied filenames: %w", err)
	}

	files, err := listSchemaFiles(schemaSource)
	if err != nil {
		return fmt.Errorf("list schema files: %w", err)
	}

	for _, filename := range files {
		if _, ok := applied[filename]; ok {
			continue
		}

		content, err := fs.ReadFile(schemaSource, filename)
		if err != nil {
			return fmt.Errorf("read %s: %w", filename, err)
		}

		checksum := fmt.Sprintf("%x", sha256.Sum256(content))

		if err := applyOne(ctx, pool, filename, string(content), checksum); err != nil {
			return fmt.Errorf("apply %s: %w", filename, err)
		}
		log.Printf("schema: applied %s", filename)
	}

	return nil
}

func applyOne(ctx context.Context, pool *pgxpool.Pool, filename, sqlText, checksum string) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, sqlText); err != nil {
		return fmt.Errorf("exec DDL: %w", err)
	}

	const insertSQL = `
		INSERT INTO schema_migrations (filename, checksum, status)
		VALUES ($1, $2, 'applied')
	`
	if _, err := tx.Exec(ctx, insertSQL, filename, checksum); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	return tx.Commit(ctx)
}

func loadAppliedFilenames(ctx context.Context, pool *pgxpool.Pool) (map[string]struct{}, error) {
	rows, err := pool.Query(ctx, `SELECT filename FROM schema_migrations WHERE status = 'applied'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var fn string
		if err := rows.Scan(&fn); err != nil {
			return nil, err
		}
		out[fn] = struct{}{}
	}
	return out, rows.Err()
}

// loadSchemaSource returns an fs.FS rooted at the schema directory.
// SCHEMA_DIR overrides the embedded FS so local dev can iterate without recompile.
func loadSchemaSource() (fs.FS, error) {
	if dir := os.Getenv("SCHEMA_DIR"); dir != "" {
		log.Printf("schema: using SCHEMA_DIR=%s", dir)
		return os.DirFS(dir), nil
	}
	sub, err := fs.Sub(postgresql.SchemaFS, "schema")
	if err != nil {
		return nil, fmt.Errorf("embedded schema sub-fs: %w", err)
	}
	return sub, nil
}

func listSchemaFiles(src fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	return files, nil
}
