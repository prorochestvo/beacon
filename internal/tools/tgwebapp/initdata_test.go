package tgwebapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// buildInitData produces a correctly-signed initData string, mirroring the
// Telegram WebApp signing algorithm exactly.
func buildInitData(botToken string, fields map[string]string, authDate int64) string {
	fields["auth_date"] = fmt.Sprintf("%d", authDate)

	keys := make([]string, 0, len(fields))
	for k := range fields {
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
		sb.WriteString(fields[k])
	}

	secretMac := hmac.New(sha256.New, []byte("WebAppData"))
	secretMac.Write([]byte(botToken))
	secretKey := secretMac.Sum(nil)

	dataMac := hmac.New(sha256.New, secretKey)
	dataMac.Write([]byte(sb.String()))
	hash := hex.EncodeToString(dataMac.Sum(nil))

	vals := url.Values{}
	for k, v := range fields {
		vals.Set(k, v)
	}
	vals.Set("hash", hash)
	return vals.Encode()
}

const (
	testBotToken = "12345:AAH"
	testUserID   = int64(999888777)
)

func validUserJSON() string {
	return fmt.Sprintf(`{"id":%d,"first_name":"Test","username":"tester"}`, testUserID)
}

func TestValidateInitData(t *testing.T) {
	t.Parallel()

	fixedNow := time.Unix(1700000000, 0)
	freshAuthDate := int64(1700000000 - 60) // 1 minute ago
	maxAge := 24 * time.Hour

	t.Run("happy path valid hash and fresh auth_date returns user id", func(t *testing.T) {
		t.Parallel()

		initData := buildInitData(testBotToken, map[string]string{
			"user": validUserJSON(),
		}, freshAuthDate)

		gotID, err := ValidateInitData(initData, testBotToken, maxAge, fixedNow)
		require.NoError(t, err)
		require.Equal(t, testUserID, gotID)
	})

	t.Run("tampered field after signing returns hash mismatch error", func(t *testing.T) {
		t.Parallel()

		initData := buildInitData(testBotToken, map[string]string{
			"user": validUserJSON(),
		}, freshAuthDate)

		// Tamper with the user field after signing.
		vals, err := url.ParseQuery(initData)
		require.NoError(t, err)
		vals.Set("user", `{"id":1,"first_name":"Attacker"}`)
		tampered := vals.Encode()

		_, err = ValidateInitData(tampered, testBotToken, maxAge, fixedNow)
		require.Error(t, err)
		require.Contains(t, err.Error(), "hash mismatch")
	})

	t.Run("missing hash field returns missing hash error", func(t *testing.T) {
		t.Parallel()

		initData := buildInitData(testBotToken, map[string]string{
			"user": validUserJSON(),
		}, freshAuthDate)

		// Strip the hash field.
		vals, err := url.ParseQuery(initData)
		require.NoError(t, err)
		vals.Del("hash")
		noHash := vals.Encode()

		_, err = ValidateInitData(noHash, testBotToken, maxAge, fixedNow)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing hash")
	})

	t.Run("auth_date older than maxAge returns expired error", func(t *testing.T) {
		t.Parallel()

		staleAuthDate := int64(1700000000 - int64(25*time.Hour/time.Second)) // 25 hours ago
		initData := buildInitData(testBotToken, map[string]string{
			"user": validUserJSON(),
		}, staleAuthDate)

		_, err := ValidateInitData(initData, testBotToken, maxAge, fixedNow)
		require.Error(t, err)
		require.Contains(t, err.Error(), "expired")
	})

	t.Run("malformed user JSON returns user parse error", func(t *testing.T) {
		t.Parallel()

		initData := buildInitData(testBotToken, map[string]string{
			"user": `not-valid-json`,
		}, freshAuthDate)

		_, err := ValidateInitData(initData, testBotToken, maxAge, fixedNow)
		require.Error(t, err)
		require.Contains(t, err.Error(), "user")
	})

	t.Run("empty initData returns error", func(t *testing.T) {
		t.Parallel()

		_, err := ValidateInitData("", testBotToken, maxAge, fixedNow)
		require.Error(t, err)
	})

	t.Run("empty botToken returns error", func(t *testing.T) {
		t.Parallel()

		initData := buildInitData(testBotToken, map[string]string{
			"user": validUserJSON(),
		}, freshAuthDate)

		_, err := ValidateInitData(initData, "", maxAge, fixedNow)
		require.Error(t, err)
	})
}
