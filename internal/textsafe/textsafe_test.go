package textsafe

import "testing"

func TestStripControls(t *testing.T) {
	if got := StripControls("normal\x1b[31mred\x07\nline"); got != "normal[31mredline" {
		t.Fatalf("StripControls() = %q", got)
	}
	const unicodeText = "keep ✓ and 中 文"
	if got := StripControls(unicodeText); got != unicodeText {
		t.Fatalf("StripControls changed ordinary Unicode text: %q", got)
	}
}
