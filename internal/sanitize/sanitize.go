// Package sanitize neutralizes strings that originate outside repull —
// container names, image names, compose labels, and error messages that may
// echo registry responses — before they are written to logs or sent as
// notifications.
package sanitize

import "strings"

// String replaces characters that can manipulate terminal output or log
// parsing with '·':
//   - C0 controls, DEL, and C1 controls — C1 includes U+009B, the 8-bit CSI
//     that some terminals honor as an ANSI escape introducer even though the
//     7-bit ESC (0x1B) is filtered
//   - U+2028/U+2029 line and paragraph separators, which some log viewers
//     render as line breaks (log-line injection)
//   - bidirectional embedding/override (U+202A–U+202E) and isolate
//     (U+2066–U+2069) characters, which can visually reorder log content
//     (spoofing)
func String(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 32 || (r >= 0x7F && r <= 0x9F): // C0, DEL, C1
			return '·'
		case r == 0x2028 || r == 0x2029: // line/paragraph separator
			return '·'
		case r >= 0x202A && r <= 0x202E: // bidi embedding/override
			return '·'
		case r >= 0x2066 && r <= 0x2069: // bidi isolates
			return '·'
		}
		return r
	}, s)
}
