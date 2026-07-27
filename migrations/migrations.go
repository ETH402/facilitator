package migrations

import "embed"

// Files contains all immutable SQL migrations.
//
//go:embed *.sql
var Files embed.FS
