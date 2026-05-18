package artificialintelligence

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ AIClient = &stubClient{}

func TestStubClient_Name(t *testing.T) {
	t.Parallel()

	t.Run("returns StubAI", func(t *testing.T) {
		t.Parallel()
		c, err := newStubAIClient("response")
		require.NoError(t, err)
		assert.Equal(t, "StubAI", c.Name())
	})
}

func TestStubClient_Model(t *testing.T) {
	t.Parallel()

	t.Run("returns stub", func(t *testing.T) {
		t.Parallel()
		c, err := newStubAIClient("response")
		require.NoError(t, err)
		assert.Equal(t, "stub", c.Model())
	})
}

func TestStubClient_CheckUP(t *testing.T) {
	t.Parallel()

	t.Run("returns nil when complete response is non-empty", func(t *testing.T) {
		t.Parallel()
		c, err := newStubAIClient("non-empty response")
		require.NoError(t, err)
		require.NoError(t, c.CheckUP(t.Context()))
	})

	t.Run("returns error when complete response is empty", func(t *testing.T) {
		t.Parallel()
		c, err := newStubAIClient("")
		require.NoError(t, err)
		assert.Error(t, c.CheckUP(t.Context()))
	})
}

func TestStubClient_Complete(t *testing.T) {
	t.Parallel()

	t.Run("returns the canned response regardless of inputs", func(t *testing.T) {
		t.Parallel()
		const canned = `{"selector_type":"css","selector":".value"}`
		c, err := newStubAIClient(canned)
		require.NoError(t, err)
		got, err := c.Complete(t.Context(), "any system prompt", "any user message")
		require.NoError(t, err)
		assert.Equal(t, canned, got)
	})
}
