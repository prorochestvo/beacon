package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRun(t *testing.T) {
	t.Parallel()

	t.Run("no args exits 2 and writes to stderr", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := run(nil, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), "doctor: expected a subcommand")
		assert.Empty(t, out.String())
	})

	t.Run("unknown subcommand exits 2 and writes to stderr", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := run([]string{"bogus"}, &out, &errOut)

		assert.Equal(t, 2, code)
		assert.Contains(t, errOut.String(), `doctor: unknown subcommand "bogus"`)
		assert.Empty(t, out.String())
	})

	t.Run("--help exits 0 and writes usage to stdout", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := run([]string{"--help"}, &out, &errOut)

		assert.Equal(t, 0, code)
		assert.Empty(t, errOut.String())
		assert.Contains(t, out.String(), "rulegen")
		assert.Contains(t, out.String(), "audit")
	})

	t.Run("-h exits 0 and writes usage to stdout", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := run([]string{"-h"}, &out, &errOut)

		assert.Equal(t, 0, code)
		assert.Empty(t, errOut.String())
	})

	t.Run("help subcommand exits 0", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := run([]string{"help"}, &out, &errOut)

		assert.Equal(t, 0, code)
		assert.Empty(t, errOut.String())
	})

	t.Run("audit --help exits 0", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := run([]string{"audit", "--help"}, &out, &errOut)

		assert.Equal(t, 0, code)
		// FlagSet writes usage to errOut; verify something was written
		combined := out.String() + errOut.String()
		assert.True(t, strings.Contains(combined, "seed-glob") || strings.Contains(combined, "Usage"),
			"expected audit usage text, got: %q", combined)
	})

	t.Run("rulegen --help exits 0", func(t *testing.T) {
		t.Parallel()

		var out, errOut bytes.Buffer
		code := run([]string{"rulegen", "--help"}, &out, &errOut)

		assert.Equal(t, 0, code)
		combined := out.String() + errOut.String()
		assert.True(t, strings.Contains(combined, "all") || strings.Contains(combined, "Usage"),
			"expected rulegen usage text, got: %q", combined)
	})
}
