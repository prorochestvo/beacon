// Package migrations exposes the canonical SQL migration files via embed.FS so
// every consumer (cmd/migrator, repository tests) reads from a single source.
package migrations

import "embed"

//go:embed *.sql

// MigrationsFS embeds all *.sql migration files from the migrations directory.
// It is consumed by cmd/migrator and by sqlitedbtest.Apply in test setups.
var MigrationsFS embed.FS
