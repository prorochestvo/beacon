package artificialintelligence

// RateExtractionRuleSchemaName is the name attached to the json_schema response
// format. It must match ^[a-zA-Z0-9_-]{1,64}$.
const RateExtractionRuleSchemaName = "rate_extraction_rule"

// RateExtractionRuleSchema returns the JSON schema describing the extraction
// rule set that the LLM must produce. The top-level object wraps a "rules"
// array so the response satisfies OpenAI strict mode (root must be an object,
// not an array). Each rule has method ∈ {regex, json} and a non-empty pattern.
//
// The schema is built fresh per call so callers cannot mutate a shared map.
//
// Note: method values "regex" and "json" map exactly to domain.MethodRegex and
// domain.MethodJSONPath. The enum is intentionally narrow — parse_float and
// store_as_rate are excluded because the executor only implements regex and json.
func RateExtractionRuleSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"rules"},
		"properties": map[string]any{
			"rules": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"method", "pattern"},
					"properties": map[string]any{
						"method": map[string]any{
							"type": "string",
							"enum": []string{"regex", "json"},
						},
						"pattern": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "expression evaluated against the response body; capture group #1 for regex, dotted path for json",
						},
					},
				},
			},
		},
	}
}
