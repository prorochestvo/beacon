package internal_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnvNamesAreDeclaredOnce pins internal/env.go as the single source of truth for
// environment variable names.
//
// They used to be re-declared per binary — thirteen declarations of six names across
// five composition roots — which reads like one greppable source of truth and is
// actually the typo-drift surface. A per-package constant means a rename can leave one
// binary reading a variable nobody sets, and a service that starts happily while
// ignoring its configuration is exactly the failure this prevents.
//
// Scanning source rather than asserting the constants' values, which would be
// tautological: the property worth defending is that no second declaration exists.
func TestEnvNamesAreDeclaredOnce(t *testing.T) {
	t.Parallel()

	literal := regexp.MustCompile(`"BEACON_[A-Z_]+"`)
	canonical := filepath.Join("internal", "env.go")

	var offenders []string
	for _, root := range []string{"cmd", "internal"} {
		err := filepath.Walk(filepath.Join("..", root), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			clean := filepath.Clean(strings.TrimPrefix(path, ".."+string(filepath.Separator)))
			if clean == canonical || clean == filepath.Join("internal", "env_test.go") {
				return nil
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, match := range literal.FindAllString(string(body), -1) {
				offenders = append(offenders, clean+": "+match)
			}
			return nil
		})
		require.NoError(t, err)
	}

	require.Emptyf(t, offenders,
		"environment variable names belong in internal/env.go and nowhere else; found:\n  %s",
		strings.Join(offenders, "\n  "))
}
