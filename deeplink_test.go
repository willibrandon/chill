package main

import (
	"strings"
	"testing"
)

// TestDeepLinkTargetsAndProviderRoutes covers direct, search, album, and playlist links.
func TestDeepLinkTargetsAndProviderRoutes(t *testing.T) {
	withConfigDir(t)
	resetProviders()
	t.Cleanup(resetProviders)
	tests := []struct {
		raw      string
		action   string
		kind     string
		provider string
		next     bool
	}{
		{"chill://play?target=https%3A%2F%2Fexample.com%2Fsong.flac", "play", "", "", false},
		{"chill://queue?provider=youtube&id=abc123&title=Song&target=https%3A%2F%2Fyoutu.be%2Fabc123&next=true", "queue", "id", "youtube", true},
		{"chill://play?provider=ytmusic&q=massive%20attack", "play", "query", "ytmusic", false},
		{"chill://queue?provider=ytmusic&album=album-id", "queue", "album", "ytmusic", false},
		{"chill://queue?provider=ytmusic&playlist=playlist-id", "queue", "playlist", "ytmusic", false},
	}
	for _, test := range tests {
		link, err := parseDeepLink(test.raw)
		if err != nil {
			t.Errorf("parse %q: %v", test.raw, err)
			continue
		}
		if link.action != test.action || link.kind != test.kind || link.provider != test.provider || link.next != test.next {
			t.Errorf("parse %q = %+v", test.raw, link)
		}
	}
}

// TestDeepLinkWithMediaExtensionRetainsSchemePriority covers CLI dispatch order.
func TestDeepLinkWithMediaExtensionRetainsSchemePriority(t *testing.T) {
	raw := "chill://play?target=https%3A%2F%2Fexample.com%2Fsong.flac"
	if !isDeepLinkInput(raw) || !isMediaInput(raw) {
		t.Fatalf("expected overlapping deep-link and media classification for %q", raw)
	}
	if _, err := parseDeepLink(raw); err != nil {
		t.Fatal(err)
	}
}

// TestDeepLinkRejectsAmbiguousAndUnsafeInput checks strict target and parameter validation.
func TestDeepLinkRejectsAmbiguousAndUnsafeInput(t *testing.T) {
	withConfigDir(t)
	resetProviders()
	t.Cleanup(resetProviders)
	invalid := []string{
		"https://example.com/song",
		"chill://stop?target=https%3A%2F%2Fexample.com%2Fsong",
		"chill://play?target=javascript%3Aalert(1)",
		"chill://play?target=https%3A%2F%2Fuser%3Apass%40example.com%2Fsong",
		"chill://play?target=https%3A%2F%2Fexample.com%2Fa&target=https%3A%2F%2Fexample.com%2Fb",
		"chill://play?target=https%3A%2F%2Fexample.com%2Fa&unknown=true",
		"chill://play?provider=youtube&id=abc&playlist=list",
		"chill://play?provider=youtube&id=abc&target=https%3A%2F%2Fevil.example%2Fabc",
		"chill://play?provider=1youtube&id=abc",
		"chill://play?provider=youtube&q=%zz",
		"chill://play?provider=youtube&q=test&title=ignored",
		"chill://play?target=https%3A%2F%2Fexample.com%2Fa&next=true",
		"chill://queue?target=https%3A%2F%2Fexample.com%2Fa&next=yes",
		"chill://play/path?target=https%3A%2F%2Fexample.com%2Fa",
		"chill://play?target=https%3A%2F%2Fexample.com%2Fa#fragment",
	}
	for _, raw := range invalid {
		if _, err := parseDeepLink(raw); err == nil {
			t.Errorf("accepted invalid link %q", raw)
		}
	}
	if _, err := parseDeepLink("chill://play?target=" + strings.Repeat("a", maxDeepLinkLength)); err == nil {
		t.Fatal("accepted oversized link")
	}
}
