// Package rulegen implements LLM-based extraction-rule generation for rate
// sources. The executor applies a chain of domain.RateSourceRule values to a
// byte body and returns the final numeric exchange-rate value.
//
// The extraction algorithm (regex/json → trim → comma-to-dot → ParseFloat →
// NaN/Inf/range guard) also appears in:
//   - internal/tools/rateextractor.RateExtractor.Run
//   - internal/application/sourceaudit.Auditor.probeSource
//
// Those are not refactored here (they carry caching, logger writes, and
// status-enum logic — separate concerns). This executor is a self-contained,
// pure third call site used only during rule generation.
package rulegen

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/seilbekskindirov/monitor/internal/domain"
	"github.com/seilbekskindirov/monitor/internal/tools/rateextractor"
)

var _ RuleExecutor = (*defaultRuleExecutor)(nil)

// RuleExecutor applies a chain of RateSourceRules to a body and returns the
// final numeric value. Implementations must be deterministic and must not
// mutate the caller's body slice.
//
// base and quote are the source pair's ISO currency codes (e.g. "USD", "KZT"),
// used for the per-pair plausibility check after the universal range check
// passes. Pass empty strings to skip the per-pair check.
type RuleExecutor interface {
	Execute(rules []domain.RateSourceRule, body []byte, base, quote string) (float64, error)
}

// NewRuleExecutor returns the default RuleExecutor implementation.
func NewRuleExecutor() RuleExecutor { return &defaultRuleExecutor{} }

type defaultRuleExecutor struct{}

// Execute runs the rule chain against body. It copies body before mutation so
// the caller can reuse the slice across calls (the generator passes the same
// body to each attempt).
//
// Post-processing: commas become dots, spaces are stripped, then the result is
// parsed as float64. NaN, Inf, and values outside (0, math.MaxInt32] are
// rejected. A per-pair plausibility check follows when base and quote are both
// non-empty and the pair is in the plausible table; unknown pairs fall through
// to the universal range only.
//
// For json rules, ApplyJSONPath decodes the full body as JSON, so a truncated
// body breaking JSON syntax produces a parse error. The supported JSON sources
// (qazpost, nationalbank) are well under 80 KB, so truncation does not hit them.
func (e *defaultRuleExecutor) Execute(rules []domain.RateSourceRule, body []byte, base, quote string) (float64, error) {
	if len(rules) == 0 {
		return 0, errors.New("rulegen: no rules")
	}

	payload := make([]byte, len(body))
	copy(payload, body)

	for i, rule := range rules {
		var (
			out []byte
			err error
		)
		switch rule.Method {
		case domain.MethodRegex:
			out, err = rateextractor.ApplyRegex(rule.Pattern, payload)
		case domain.MethodJSONPath:
			out, err = rateextractor.ApplyJSONPath(rule.Pattern, payload)
		default:
			return 0, fmt.Errorf("rulegen: unsupported method %q at rule %d", rule.Method, i)
		}
		if err != nil {
			return 0, fmt.Errorf("rulegen: rule %d (%s): %w", i, rule.Method, err)
		}
		payload = bytes.TrimSpace(out)
	}

	payload = bytes.ReplaceAll(payload, []byte(","), []byte("."))
	payload = bytes.ReplaceAll(payload, []byte(" "), []byte(""))

	v, err := strconv.ParseFloat(string(payload), 64)
	if err != nil {
		return 0, fmt.Errorf("rulegen: parse %q: %w", string(payload), err)
	}

	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("rulegen: NaN or Inf")
	}

	if v <= rateextractor.MinPlausibleRateValue || v > rateextractor.MaxPlausibleRateValue {
		return 0, fmt.Errorf("rulegen: value %g outside plausible range (%g, %g]",
			v, rateextractor.MinPlausibleRateValue, float64(rateextractor.MaxPlausibleRateValue))
	}

	if lo, hi, ok := plausibleRangeFor(base, quote); ok {
		if v < lo || v > hi {
			return 0, fmt.Errorf(
				"rulegen: value %g rejected: outside plausible range [%g, %g] for %s/%s",
				v, lo, hi, base, quote,
			)
		}
	}

	return v, nil
}
