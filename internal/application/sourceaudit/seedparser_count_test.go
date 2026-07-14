package sourceaudit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seilbekskindirov/beacon/internal/application/sourceaudit"
	"github.com/seilbekskindirov/beacon/migrations"
)

func TestParseSeedFiles_EmbeddedMigrations(t *testing.T) {
	t.Parallel()

	t.Run("embedded migrations enumerate 56 sources", func(t *testing.T) {
		t.Parallel()
		sources, err := sourceaudit.ParseSeedFiles(migrations.MigrationsFS, "*.seed*.sql")
		require.NoError(t, err)
		assert.Len(t, sources, 56)

		// A duplicated seed name is silently dropped by INSERT OR IGNORE at
		// migrate time, so the DB ends up one row short while this count stays
		// green. Guard against that: every parsed name must be unique.
		seen := make(map[string]string, len(sources))
		for _, s := range sources {
			if origin, ok := seen[s.Name]; ok {
				assert.Failf(t, "duplicate seed source name", "name %q parsed from %q duplicates the one from %q; INSERT OR IGNORE would silently drop it", s.Name, s.Origin, origin)
				continue
			}
			seen[s.Name] = s.Origin
		}
	})
}
