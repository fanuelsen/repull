package sanitize

import "testing"

func TestString(t *testing.T) {
	wrap := func(r rune) string { return "a" + string(r) + "b" }

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain ascii unchanged", in: "myproject:web", want: "myproject:web"},
		{name: "unicode text unchanged", in: "café-web", want: "café-web"},
		{name: "newline", in: "a\nb", want: "a·b"},
		{name: "carriage return", in: "a\rb", want: "a·b"},
		{name: "tab", in: "a\tb", want: "a·b"},
		{name: "escape (7-bit ANSI)", in: "a\x1b[31mred", want: "a·[31mred"},
		{name: "del", in: wrap(0x7F), want: "a·b"},
		{name: "C1 CSI (8-bit ANSI)", in: wrap(0x9B), want: "a·b"},
		{name: "C1 NEL", in: wrap(0x85), want: "a·b"},
		{name: "line separator", in: wrap(0x2028), want: "a·b"},
		{name: "paragraph separator", in: wrap(0x2029), want: "a·b"},
		{name: "bidi RLO override", in: wrap(0x202E), want: "a·b"},
		{name: "bidi LRI isolate", in: wrap(0x2066), want: "a·b"},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := String(tt.in); got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
