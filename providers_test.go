package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestProviderBaseURLSecurity rejects credential leakage and clear-text remote accounts.
func TestProviderBaseURLSecurity(t *testing.T) {
	for _, raw := range []string{"https://user:secret@example.com", "http://example.com", "file:///tmp/music"} {
		provider := newHTTPMediaProvider("test", providerConfig{Type: "jellyfin", URL: raw}, providerSecret{Token: "token"})
		if _, err := provider.baseURL(); err == nil {
			t.Errorf("accepted provider URL %q", raw)
		}
	}
	provider := newHTTPMediaProvider("test", providerConfig{Type: "jellyfin", URL: "http://127.0.0.1:8096"}, providerSecret{Token: "token"})
	if _, err := provider.baseURL(); err != nil {
		t.Fatalf("rejected loopback provider: %v", err)
	}
}

// TestProviderArtworkIsCachedWithoutPersistingCredentials checks authenticated metadata handling.
func TestProviderArtworkIsCachedWithoutPersistingCredentials(t *testing.T) {
	withConfigDir(t)
	const token = "private-access-token"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/Items/song/Images/Primary" {
			t.Errorf("artwork path = %q", request.URL.Path)
		}
		if request.URL.RawQuery != "" || strings.Contains(request.URL.String(), token) {
			t.Errorf("credential appeared in artwork URL %q", request.URL.String())
		}
		if request.Header.Get("X-Emby-Token") != token {
			t.Errorf("authentication header = %q", request.Header.Get("X-Emby-Token"))
		}
		response.Header().Set("Content-Type", "image/png")
		_, _ = response.Write([]byte("\x89PNG\r\n\x1a\nfixture"))
	}))
	defer server.Close()
	provider := newHTTPMediaProvider("jellyfin", providerConfig{Type: "jellyfin", URL: server.URL}, providerSecret{Token: token})
	item := providerMediaItem("jellyfin", "song", "Song", "Artist", "Album", "", "", 120)
	item.ProviderMeta["image_id"] = "song"
	path, err := provider.loadArtwork(context.Background(), item)
	if err != nil || !strings.HasSuffix(path, ".png") {
		t.Fatalf("cached artwork = %q, %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(data), "\x89PNG") {
		t.Fatalf("cached bytes = %q, %v", data, err)
	}
	stream, err := provider.resolve(context.Background(), item)
	if err != nil || strings.Contains(stream.URL, token) || stream.Headers["X-Emby-Token"] != token {
		t.Fatalf("stream = %+v, %v", stream, err)
	}
}

// TestProviderSearchIdentityDeduplicatesEquivalentCatalogTracks checks global result merging.
func TestProviderSearchIdentityDeduplicatesEquivalentCatalogTracks(t *testing.T) {
	one := providerMediaItem("one", "1", "  Teardrop ", "Massive Attack", "", "", "", 0)
	two := providerMediaItem("two", "2", "teardrop", "massive   attack", "", "", "", 0)
	if providerSearchIdentity(one) != providerSearchIdentity(two) {
		t.Fatalf("identities differ: %q != %q", providerSearchIdentity(one), providerSearchIdentity(two))
	}
}

// TestMixcloudSearchUsesNativeCatalog verifies stable metadata and paging.
func TestMixcloudSearchUsesNativeCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/search/" {
			t.Errorf("search path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("q") != "ambient" || request.URL.Query().Get("type") != "cloudcast" || request.URL.Query().Get("limit") != "2" || request.URL.Query().Get("offset") != "4" {
			t.Errorf("search query = %q", request.URL.RawQuery)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"data":[{"key":"/lowlight/positively-ambient/","name":"Positively ambient","audio_length":3469,"pictures":{"extra_large":"https://images.example/art.jpg"},"user":{"name":"low light mixes","username":"lowlight"}}],"paging":{"next":"https://api.mixcloud.com/search/?offset=6"}}`))
	}))
	defer server.Close()

	provider := newYTDLPProvider("mixcloud", providerConfig{Type: "mixcloud"})
	provider.apiURL, provider.client = server.URL+"/", server.Client()
	items, next, err := provider.searchMixcloud(t.Context(), "ambient", 2, 4)
	if err != nil || next != 5 || len(items) != 1 {
		t.Fatalf("Mixcloud search = %+v, next %d, %v", items, next, err)
	}
	item := items[0]
	if item.ProviderID != "lowlight/positively-ambient" || item.Title != "Positively ambient" || item.Artist != "low light mixes" || item.Duration != 3469 || item.Artwork != "https://images.example/art.jpg" || item.ProviderMeta["url"] != "https://www.mixcloud.com/lowlight/positively-ambient/" {
		t.Fatalf("Mixcloud item = %+v", item)
	}
}

// TestMixcloudItemsRemainResolvable verifies durable provider identities.
func TestMixcloudItemsRemainResolvable(t *testing.T) {
	provider := newYTDLPProvider("mixcloud", providerConfig{Type: "mixcloud"})
	item := providerMediaItem("mixcloud", "listener/show", "Show", "Listener", "", "", "", 0)
	stream, err := provider.resolve(t.Context(), item)
	if err != nil || stream.URL != "https://www.mixcloud.com/listener/show/" {
		t.Fatalf("Mixcloud stream = %+v, %v", stream, err)
	}
}

// TestAudiobookshelfLookupRestoresCompositeTrackIDs checks saved queue items
// remain resolvable after the in-memory provider result cache is gone.
func TestAudiobookshelfLookupRestoresCompositeTrackIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/items/book" {
			t.Errorf("lookup path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"book","media":{"metadata":{"title":"Novel","authorName":"Author"},"tracks":[{"ino":"7","title":"Chapter","duration":60}]}}`))
	}))
	defer server.Close()
	provider := newHTTPMediaProvider("audiobookshelf", providerConfig{Type: "audiobookshelf", URL: server.URL}, providerSecret{Token: "token"})
	item, err := provider.lookup(t.Context(), "book:7")
	if err != nil || item.ProviderID != "book:7" || item.Title != "Chapter" || item.ProviderMeta["item_id"] != "book" {
		t.Fatalf("lookup = %+v, %v", item, err)
	}
}
