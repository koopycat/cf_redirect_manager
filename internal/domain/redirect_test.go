package domain

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		r    Redirect
		ok   bool
	}{
		{"valid default", New("https://example.com/a", "https://new.example/a"), true},
		{"valid status", Redirect{Source: "http://example.com", Target: "https://example.org", StatusCode: 308}, true},
		{"valid schemeless source", New("example.com/blog/", "https://example.org/blog/"), true},
		{"relative source", New("/a", "https://example.org"), false},
		{"schemeless target", New("example.com/a", "example.org"), false},
		{"unsupported scheme", New("ftp://example.com/a", "https://example.org"), false},
		{"source user info", New("https://user@example.com/a", "https://example.org"), false},
		{"target user info", New("example.com/a", "https://user@example.org"), false},
		{"fragment", New("https://example.com/a#x", "https://example.org"), false},
		{"control character", New("example.com/a\x1b[2J", "https://example.org"), false},
		{"bad status", Redirect{Source: "https://example.com", Target: "https://example.org", StatusCode: 200}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Validate() == nil; got != tt.ok {
				t.Fatalf("Validate success = %v, want %v", got, tt.ok)
			}
		})
	}
}

func TestEqualContentNormalizesDefaultStatusAndIgnoresID(t *testing.T) {
	a := Redirect{ID: "one", Source: "https://a.example", Target: "https://b.example"}
	b := a
	b.ID = "two"
	b.StatusCode = 301
	if !a.EqualContent(b) {
		t.Fatal("expected equivalent redirects")
	}
	b.Comment = "different"
	if a.EqualContent(b) {
		t.Fatal("comment difference must be detected")
	}
}
