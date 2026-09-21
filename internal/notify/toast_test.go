package notify

import (
	"encoding/xml"
	"strings"
	"testing"
)

// TestBuildToastXML checks escaping, artwork, and sound behavior.
func TestBuildToastXML(t *testing.T) {
	result := buildToastXML(`A & B <live>`, `From "Studio"`, "https://example.com/art?a=1&b=2")
	if !strings.Contains(result, "A &amp; B &lt;live&gt;") {
		t.Fatalf("title was not escaped: %s", result)
	}
	if !strings.Contains(result, `src="https://example.com/art?a=1&amp;b=2"`) {
		t.Fatalf("artwork was not escaped: %s", result)
	}
	if !strings.Contains(result, `<audio silent="true"></audio>`) {
		t.Fatalf("toast is not silent: %s", result)
	}
	var document toastDocument
	if err := xml.Unmarshal([]byte(result), &document); err != nil {
		t.Fatalf("invalid XML: %v", err)
	}
}

// TestBuildToastXMLRejectsUnsupportedArtwork keeps non-URL data out of toasts.
func TestBuildToastXMLRejectsUnsupportedArtwork(t *testing.T) {
	result := buildToastXML("Track", "Artist", "data:image/png;base64,nope")
	if strings.Contains(result, "<image") {
		t.Fatalf("unsupported artwork was included: %s", result)
	}
}

// TestBuildToastXMLReplacesInvalidCharacters ensures metadata always produces valid XML.
func TestBuildToastXMLReplacesInvalidCharacters(t *testing.T) {
	result := buildToastXML("Track\x00Name", "Artist", "")
	if strings.ContainsRune(result, '\x00') || !strings.Contains(result, "Track�Name") {
		t.Fatalf("invalid XML character was not replaced: %q", result)
	}
}
