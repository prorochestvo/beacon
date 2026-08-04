// Package migrations exposes the canonical SQL migration files via embed.FS so
// every consumer (cmd/migrator, repository tests) reads from a single source.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql

// MigrationsFS embeds all *.sql migration files from the migrations directory.
// It is consumed by cmd/migrator and by sqlitedbtest.Apply in test setups.
var MigrationsFS embed.FS

//go:embed archive/*.sql

// archiveFS embeds the archive tier's migration chain. It is kept unexported and
// surfaced through ArchiveMigrationsFS below: the migrator reads its migration
// directory with fs.ReadDir(fsys, "."), so an FS whose files sit under archive/
// would silently yield zero migrations rather than an error.
var archiveFS embed.FS

// ArchiveMigrationsFS is the archive database's migration chain, re-rooted so its
// files appear at ".". The archive is a separate SQLite file with its own schema and
// its own __schema_migrations ledger: it holds every value ever recorded, while the
// hot database keeps only a bounded recent window.
var ArchiveMigrationsFS fs.FS = mustSub(archiveFS, "archive")

// mustSub panics on a broken embed path. Its argument is a compile-time constant
// matched against a compile-time embed directive, so a failure here is a build
// mistake that reached a package variable, not a runtime condition worth returning.
func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic("migrations: embed sub " + dir + ": " + err.Error())
	}
	return sub
}
