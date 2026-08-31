// Package textsafe removes characters that can alter terminal output.
package textsafe

import (
	"strings"
	"unicode"
)

// StripControls removes Unicode control characters, including terminal escape
// characters, while leaving ordinary text unchanged.
func StripControls(value string) string {
	if !strings.ContainsFunc(value, unicode.IsControl) {
		return value
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}
