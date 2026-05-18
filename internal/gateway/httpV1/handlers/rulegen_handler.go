package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/seilbekskindirov/monitor/internal"
	"github.com/seilbekskindirov/monitor/internal/application/rulegen"
	"github.com/seilbekskindirov/monitor/pkg/api"
)

// rulegenGenerator is the narrow interface the handler needs from the
// application-layer generator. Defined inside the handlers package so
// tests can substitute a fake without depending on rulegen's full surface.
type rulegenGenerator interface {
	Generate(ctx context.Context, sourceName string, forceFallback bool) (*rulegen.Result, error)
}

// RulegenGenerator is the exported alias used by the composition root
// (cmd/web/main.go, gateway.go, router.go) to thread the generator through
// without importing the full rulegen package.
type RulegenGenerator = rulegenGenerator

// rulegenGeneratorFactory builds a Generator with caller-supplied attempt
// budgets. The handler invokes this per-request only when overrides are
// supplied; the default-budget Generator is cached on the Handler.
type rulegenGeneratorFactory func(maxPrimary, maxFallback int) (rulegenGenerator, error)

// RulegenGeneratorFactory is the exported alias for the factory type used by
// the composition root.
type RulegenGeneratorFactory = rulegenGeneratorFactory

// rulegenRequestTimeout is the per-request hard ceiling for the generate
// endpoint. Declared as var so tests can substitute a shorter duration.
var rulegenRequestTimeout = 120 * time.Second

const (
	defaultMaxPrimary  = 3
	defaultMaxFallback = 2
)

// GenerateRules triggers the rule-generator audit loop for a named source.
//
// POST /api/sources/{name}/rules/generate
// Auth: X-Telegram-Init-Data header (or ?initData= query fallback).
// Authorisation: the authenticated Telegram user id must equal the
// configured admin chat id.
//
// Request body (JSON, all fields optional):
//
//	{ "force_fallback": false, "max_primary_attempts": 3, "max_fallback_attempts": 2 }
//
// HTTP status mapping:
//
//	200 — success (body: RulegenResponse)
//	400 — malformed JSON body / missing source name / override out of [1,10]
//	401 — missing or invalid initData
//	403 — authenticated user is not the admin
//	404 — source name does not exist (errors.Is ErrSourceNotFound)
//	409 — concurrent generation already in progress for this source
//	502 — all attempts exhausted (errors.Is ErrAttemptsExhausted)
//	503 — required fetcher not available in this build (errors.Is ErrUnsupportedFetcherKind)
//	504 — request deadline exceeded (120s)
//	500 — any other internal failure
func (h *Handler) GenerateRules(w http.ResponseWriter, r *http.Request) {
	name, err := extractName(r)
	if err != nil {
		http.Error(w, `{"error":"missing source name"}`, http.StatusBadRequest)
		return
	}

	initData := r.Header.Get("X-Telegram-Init-Data")
	if initData == "" {
		initData = r.URL.Query().Get("initData")
	}
	userID, err := h.validateInitData(initData, h.botToken, meSubscriptionsMaxAge, h.nowFn())
	if err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if userID != h.adminChatID {
		http.Error(w, `{"error":"This endpoint is admin-only"}`, http.StatusForbidden)
		return
	}

	var req api.RulegenRequest
	if r.Body != nil {
		if decErr := json.NewDecoder(r.Body).Decode(&req); decErr != nil && !errors.Is(decErr, io.EOF) {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
	}
	if req.MaxPrimaryAttempts < 0 || req.MaxPrimaryAttempts > 10 {
		http.Error(w, `{"error":"max_primary_attempts must be between 1 and 10"}`, http.StatusBadRequest)
		return
	}
	if req.MaxFallbackAttempts < 0 || req.MaxFallbackAttempts > 10 {
		http.Error(w, `{"error":"max_fallback_attempts must be between 1 and 10"}`, http.StatusBadRequest)
		return
	}

	release, ok := h.lockMgr.TryAcquire(name)
	if !ok {
		http.Error(w, `{"error":"rule generation already in progress for this source"}`, http.StatusConflict)
		return
	}
	defer release()

	gen := h.defaultGenerator
	if req.MaxPrimaryAttempts > 0 || req.MaxFallbackAttempts > 0 {
		mp := req.MaxPrimaryAttempts
		if mp == 0 {
			mp = defaultMaxPrimary
		}
		mf := req.MaxFallbackAttempts
		if mf == 0 {
			mf = defaultMaxFallback
		}
		custom, buildErr := h.generatorFactory(mp, mf)
		if buildErr != nil {
			h.internalError(w, buildErr)
			return
		}
		gen = custom
	}

	ctx, cancel := context.WithTimeout(r.Context(), rulegenRequestTimeout)
	defer cancel()

	result, err := gen.Generate(ctx, name, req.ForceFallback)
	if err != nil {
		h.writeRulegenError(w, name, err, ctx.Err())
		return
	}

	writeJSON(w, api.RulegenResponse{
		Source:       result.Source.Name,
		Value:        result.Value,
		Rules:        result.Rules,
		AttemptsUsed: result.AttemptsUsed,
		Escalated:    result.Escalated,
		Provider:     result.Metadata.Provider,
		Model:        result.Metadata.Model,
		GeneratedAt:  result.Metadata.GeneratedAt,
	})
}

// writeRulegenError maps known rulegen error kinds to specific HTTP status
// codes and safe public messages. The full error is logged for ops visibility.
func (h *Handler) writeRulegenError(w http.ResponseWriter, sourceName string, err error, ctxErr error) {
	log.Print(errors.Join(
		fmt.Errorf("generate rules for %q: %w", sourceName, err),
		internal.NewTraceError(),
	))

	switch {
	case ctxErr != nil && errors.Is(ctxErr, context.DeadlineExceeded):
		http.Error(w, `{"error":"rule generation timed out"}`, http.StatusGatewayTimeout)
	case errors.Is(err, rulegen.ErrSourceNotFound):
		http.Error(w, `{"error":"source not found"}`, http.StatusNotFound)
	case errors.Is(err, rulegen.ErrUnsupportedFetcherKind):
		http.Error(w, `{"error":"required fetcher is not available in this build"}`, http.StatusServiceUnavailable)
	case errors.Is(err, rulegen.ErrAttemptsExhausted):
		body, marshalErr := json.Marshal(map[string]string{
			"error": "rule generation failed for source " + sourceName,
		})
		if marshalErr != nil {
			h.internalError(w, marshalErr)
			return
		}
		http.Error(w, string(body), http.StatusBadGateway)
	default:
		h.internalError(w, err)
	}
}
