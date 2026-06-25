// Package tgwebapp provides helpers for verifying Telegram Mini App (WebApp) initData.
package tgwebapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ValidateInitData verifies that initData was signed by Telegram with botToken,
// returns the parsed user.id, and rejects payloads older than maxAge.
// Uses HMAC-SHA256 with secret_key = HMAC_SHA256("WebAppData", botToken).
//
// now is injected for deterministic tests — pass time.Now() in production.
func ValidateInitData(initData, botToken string, maxAge time.Duration, now time.Time) (int64, error) {
	if initData == "" {
		return 0, errors.New("tgwebapp: empty initData")
	}
	if botToken == "" {
		return 0, errors.New("tgwebapp: empty botToken")
	}

	values, err := url.ParseQuery(initData)
	if err != nil {
		return 0, fmt.Errorf("tgwebapp: parse initData: %w", err)
	}

	gotHash := values.Get("hash")
	if gotHash == "" {
		return 0, errors.New("tgwebapp: missing hash")
	}
	values.Del("hash")

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(values.Get(k))
	}

	// secret_key = HMAC_SHA256("WebAppData", botToken): "WebAppData" is the key,
	// botToken is the message — the order required by the Telegram WebApp spec.
	secretMac := hmac.New(sha256.New, []byte("WebAppData"))
	secretMac.Write([]byte(botToken))
	secretKey := secretMac.Sum(nil)

	dataMac := hmac.New(sha256.New, secretKey)
	dataMac.Write([]byte(sb.String()))
	expected := hex.EncodeToString(dataMac.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(expected), []byte(gotHash)) != 1 {
		return 0, errors.New("tgwebapp: hash mismatch")
	}

	authUnix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("tgwebapp: invalid auth_date: %w", err)
	}
	if age := now.Sub(time.Unix(authUnix, 0)); age > maxAge {
		return 0, fmt.Errorf("tgwebapp: expired (age %s > %s)", age, maxAge)
	}

	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil {
		return 0, fmt.Errorf("tgwebapp: parse user: %w", err)
	}
	if user.ID == 0 {
		return 0, errors.New("tgwebapp: user.id is missing or zero")
	}

	return user.ID, nil
}
