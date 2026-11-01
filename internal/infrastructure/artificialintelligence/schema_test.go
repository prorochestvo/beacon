package artificialintelligence

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateExtractionRuleSchema(t *testing.T) {
	t.Parallel()

	t.Run("schema name matches the required regex", func(t *testing.T) {
		t.Parallel()
		re := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
		assert.True(t, re.MatchString(RateExtractionRuleSchemaName),
			"schema name %q must match ^[a-zA-Z0-9_-]{1,64}$", RateExtractionRuleSchemaName)
	})

	t.Run("returns a fresh map each call so callers cannot mutate shared state", func(t *testing.T) {
		t.Parallel()
		a := RateExtractionRuleSchema()
		b := RateExtractionRuleSchema()
		a["injected_sentinel"] = "should_not_bleed"
		bVal, exists := b["injected_sentinel"]
		assert.False(t, exists, "mutation of one schema map must not affect another; got %v", bVal)
	})

	t.Run("root is object with additionalProperties false and required rules", func(t *testing.T) {
		t.Parallel()
		s := RateExtractionRuleSchema()
		assert.Equal(t, "object", s["type"])
		assert.Equal(t, false, s["additionalProperties"])
		required, ok := s["required"].([]string)
		require.True(t, ok, "required must be []string")
		assert.Equal(t, []string{"rules"}, required)
	})

	t.Run("rules array item has method enum regex and json", func(t *testing.T) {
		t.Parallel()
		s := RateExtractionRuleSchema()
		props, ok := s["properties"].(map[string]any)
		require.True(t, ok, "properties must be map[string]any")

		rules, ok := props["rules"].(map[string]any)
		require.True(t, ok, "rules must be map[string]any")
		assert.Equal(t, "array", rules["type"])

		items, ok := rules["items"].(map[string]any)
		require.True(t, ok, "items must be map[string]any")
		assert.Equal(t, "object", items["type"])
		assert.Equal(t, false, items["additionalProperties"])

		itemRequired, ok := items["required"].([]string)
		require.True(t, ok, "item required must be []string")
		assert.Contains(t, itemRequired, "method")
		assert.Contains(t, itemRequired, "pattern")

		itemProps, ok := items["properties"].(map[string]any)
		require.True(t, ok)

		methodSchema, ok := itemProps["method"].(map[string]any)
		require.True(t, ok, "method schema must be map[string]any")
		assert.Equal(t, "string", methodSchema["type"])
		methodEnum, ok := methodSchema["enum"].([]string)
		require.True(t, ok, "method enum must be []string")
		assert.Equal(t, []string{"regex", "json"}, methodEnum)

		patternSchema, ok := itemProps["pattern"].(map[string]any)
		require.True(t, ok, "pattern schema must be map[string]any")
		assert.Equal(t, "string", patternSchema["type"])
	})

	t.Run("stub default response is valid JSON conforming to the schema", func(t *testing.T) {
		t.Parallel()
		var parsed map[string]any
		err := json.Unmarshal([]byte(stubAIDefaultResponse), &parsed)
		require.NoError(t, err, "stubAIDefaultResponse must be valid JSON")

		rules, ok := parsed["rules"]
		require.True(t, ok, "stubAIDefaultResponse must contain 'rules' field")

		rulesSlice, ok := rules.([]any)
		require.True(t, ok, "rules must be an array")
		require.NotEmpty(t, rulesSlice, "rules must not be empty")

		firstRule, ok := rulesSlice[0].(map[string]any)
		require.True(t, ok, "first rule must be an object")
		assert.Contains(t, firstRule, "method", "rule must have method")
		assert.Contains(t, firstRule, "pattern", "rule must have pattern")

		method, ok := firstRule["method"].(string)
		require.True(t, ok, "method must be a string")
		assert.Contains(t, []string{"regex", "json"}, method, "method must be in the enum")
	})
}
