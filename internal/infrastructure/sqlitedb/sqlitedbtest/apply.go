// Package sqlitedbtest provides test helpers for packages that need a
// fully-migrated SQLite schema.
package sqlitedbtest

import (
	"context"
	"testing"

	"github.com/seilbekskindirov/beacon/internal/infrastructure/sqlitedb"
	"github.com/seilbekskindirov/beacon/migrations"
	"github.com/stretchr/testify/require"
)

// Apply runs every migration in the canonical migrations.MigrationsFS against db.
// It is intended for use in test setup before any repository operations are performed.
// Accepts testing.TB so it works in both Test* and Benchmark* functions.
func Apply(tb testing.TB, db sqlitedb.Committer) {
	tb.Helper()

	m, err := sqlitedb.NewMigrator(db, migrations.MigrationsFS)
	require.NoError(tb, err)
	require.NoError(tb, m.Run(context.Background()))
}
