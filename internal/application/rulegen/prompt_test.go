package rulegen

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSystemPrompt(t *testing.T) {
	t.Parallel()

	t.Run("is non-empty", func(t *testing.T) {
		t.Parallel()
		assert.NotEmpty(t, systemPrompt)
	})

	t.Run("contains regex method reference", func(t *testing.T) {
		t.Parallel()
		assert.Contains(t, systemPrompt, `"regex"`)
	})

	t.Run("contains json method reference", func(t *testing.T) {
		t.Parallel()
		assert.Contains(t, systemPrompt, `"json"`)
	})

	t.Run("contains rate_extraction_rule schema name reference", func(t *testing.T) {
		t.Parallel()
		assert.Contains(t, systemPrompt, "rate_extraction_rule")
	})

	t.Run("contains PREVIOUS ATTEMPTS section marker", func(t *testing.T) {
		t.Parallel()
		assert.Contains(t, systemPrompt, "PREVIOUS ATTEMPTS")
	})

	t.Run("is under 4 KB to avoid burning tokens", func(t *testing.T) {
		t.Parallel()
		assert.Less(t, len(systemPrompt), 4*1024, "systemPrompt must be under 4 KB")
	})

	t.Run("forbids RE2 repeat counts above 1000", func(t *testing.T) {
		t.Parallel()
		assert.Contains(t, systemPrompt, "repeat counts")
		assert.Contains(t, systemPrompt, "1000")
	})

	t.Run("contains BID/ASK semantics block", func(t *testing.T) {
		t.Parallel()
		assert.Contains(t, systemPrompt, "BID/ASK SEMANTICS")
		assert.Contains(t, systemPrompt, "Сату")
		assert.Contains(t, systemPrompt, "the LARGER number is ASK")
	})
}
