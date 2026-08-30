package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceMetaRepository(t *testing.T) {
	t.Parallel()

	t.Run("an unwritten key reports absent, not an error", func(t *testing.T) {
		t.Parallel()
		repo, err := NewServiceMetaRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		// "never vacuumed" is the state of every fresh deployment; treating it as a
		// failure would make the first maintenance pass log an error on every new host.
		value, ok, err := repo.ObtainServiceMeta(t.Context(), ServiceMetaKeyLastVacuum)
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Empty(t, value)
	})

	t.Run("a value round-trips", func(t *testing.T) {
		t.Parallel()
		repo, err := NewServiceMetaRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		require.NoError(t, repo.RetainServiceMeta(t.Context(), ServiceMetaKeyLastVacuum, "2026-08-05T12:00:00Z"))
		value, ok, err := repo.ObtainServiceMeta(t.Context(), ServiceMetaKeyLastVacuum)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "2026-08-05T12:00:00Z", value)
	})

	t.Run("writing the same key replaces rather than duplicating", func(t *testing.T) {
		t.Parallel()
		db := stubSQLiteDB(t)
		repo, err := NewServiceMetaRepository(db)
		require.NoError(t, err)

		require.NoError(t, repo.RetainServiceMeta(t.Context(), ServiceMetaKeyLastVacuum, "first"))
		require.NoError(t, repo.RetainServiceMeta(t.Context(), ServiceMetaKeyLastVacuum, "second"))

		value, _, err := repo.ObtainServiceMeta(t.Context(), ServiceMetaKeyLastVacuum)
		require.NoError(t, err)
		assert.Equal(t, "second", value)
		assert.Equal(t, int64(1), countIn(t, db, serviceMetaTableName))
	})

	t.Run("keys are independent", func(t *testing.T) {
		t.Parallel()
		repo, err := NewServiceMetaRepository(stubSQLiteDB(t))
		require.NoError(t, err)

		require.NoError(t, repo.RetainServiceMeta(t.Context(), "a", "1"))
		require.NoError(t, repo.RetainServiceMeta(t.Context(), "b", "2"))

		a, _, err := repo.ObtainServiceMeta(t.Context(), "a")
		require.NoError(t, err)
		assert.Equal(t, "1", a)
	})

	t.Run("an empty key is rejected", func(t *testing.T) {
		t.Parallel()
		repo, err := NewServiceMetaRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		require.Error(t, repo.RetainServiceMeta(t.Context(), "", "value"))
	})

	t.Run("CheckUP reads the table", func(t *testing.T) {
		t.Parallel()
		repo, err := NewServiceMetaRepository(stubSQLiteDB(t))
		require.NoError(t, err)
		require.NoError(t, repo.CheckUP(t.Context()))
		assert.Equal(t, "service_meta", repo.Name())
	})
}
