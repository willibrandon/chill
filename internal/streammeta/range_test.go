package streammeta

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFiniteRangesValidateRepresentation checks valid, ignored, and inconsistent ranges.
func TestFiniteRangesValidateRepresentation(t *testing.T) {
	for _, mode := range []string{"valid", "ignored", "wrong-offset", "changed"} {
		t.Run(mode, func(t *testing.T) {
			const data = "0123456789abcdef"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("ETag", `"original"`)
				if r.Header.Get("Range") == "" || mode == "ignored" {
					fmt.Fprint(w, data)
					return
				}
				if r.Header.Get("If-Range") != `"original"` {
					t.Error("missing representation validator")
				}
				if mode == "changed" {
					w.Header().Set("ETag", `"changed"`)
				}
				start := 8
				if mode == "wrong-offset" {
					start = 7
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-15/16", start))
				w.WriteHeader(http.StatusPartialContent)
				fmt.Fprint(w, data[start:])
			}))
			defer server.Close()
			source, err := OpenFinite(t.Context(), server.URL, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Body.Close()
			seeker, ok := source.Body.(io.Seeker)
			if !ok {
				t.Fatal("range source is not seekable")
			}
			_, err = seeker.Seek(8, io.SeekStart)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(source.Body)
				if err != nil || string(got) != data[8:] {
					t.Fatalf("%q %v", got, err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid range accepted")
				}
				got, err := io.ReadAll(source.Body)
				if err != nil || strings.TrimSpace(string(got)) != data {
					t.Fatalf("original response lost: %q %v", got, err)
				}
			}
		})
	}
}
