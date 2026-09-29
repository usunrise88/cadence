// Package migrations holds the forward-only SQL migrations embedded in the binary.
//
// Files are named NNNN_name.sql and applied in order by internal/storage at start. Never edit or delete a file
// that has shipped: add a new one (expand-and-contract).
package migrations

import "embed"

// FS contains every *.sql migration.
//
//go:embed *.sql
var FS embed.FS
