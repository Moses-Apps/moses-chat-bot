// Package postgresql provides embedded SQL schema files for database initialization.
//
// Schema files are numbered (001_, 002_, etc.) and applied in alphanumeric order
// by db.ApplySchema. The binary is fully self-contained — no filesystem path
// resolution at runtime. Set SCHEMA_DIR to override with a filesystem path for
// local iteration without rebuilding.
package postgresql

import "embed"

// SchemaFS contains all numbered SQL schema files embedded at compile time.
//
//go:embed schema/*.sql
var SchemaFS embed.FS
