package dom

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEscape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "no special characters",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "ampersand",
			input: "Tom & Jerry",
			want:  "Tom &amp; Jerry",
		},
		{
			name:  "less-than",
			input: "<div>",
			want:  "&lt;div&gt;",
		},
		{
			name:  "greater-than",
			input: "x > y",
			want:  "x &gt; y",
		},
		{
			name:  "double quote",
			input: `say "hello"`,
			want:  "say &quot;hello&quot;",
		},
		{
			name:  "single quote is not escaped",
			input: "it's fine",
			want:  "it's fine",
		},
		{
			name:  "XSS script tag",
			input: `<script>alert(1)</script>`,
			want:  "&lt;script&gt;alert(1)&lt;/script&gt;",
		},
		{
			name:  "all four special characters together",
			input: `<a href="x&y">`,
			want:  "&lt;a href=&quot;x&amp;y&quot;&gt;",
		},
		{
			name:  "ampersand first to avoid double-escaping",
			input: "&<>\"",
			want:  "&amp;&lt;&gt;&quot;",
		},
		{
			name:  "already-entity-looking string is not double-escaped",
			input: "&amp;",
			want:  "&amp;amp;",
		},
		{
			name:  "combination with single quote preserved",
			input: `He said "it's a <trap>" & left`,
			want:  "He said &quot;it's a &lt;trap&gt;&quot; &amp; left",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, Escape(tc.input))
		})
	}
}
