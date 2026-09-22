package main

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	providerDocumentVersion = 1
	mixcloudAPIURL          = "https://api.mixcloud.com/"
)

type providerConfig struct {
	Type        string   `json:"type"`                   // Type selects the provider implementation.
	Enabled     bool     `json:"enabled"`                // Enabled makes the provider available.
	URL         string   `json:"url,omitempty"`          // URL is a self-hosted server base address.
	Username    string   `json:"username,omitempty"`     // Username identifies a server account.
	UserID      string   `json:"user_id,omitempty"`      // UserID optionally avoids server-side discovery.
	ClientID    string   `json:"client_id,omitempty"`    // ClientID identifies an OAuth application.
	Country     string   `json:"country,omitempty"`      // Country selects regional catalog results.
	Quality     string   `json:"quality,omitempty"`      // Quality selects the provider stream tier.
	CookiesFrom string   `json:"cookies_from,omitempty"` // CookiesFrom names a browser cookie store.
	Libraries   []string `json:"libraries,omitempty"`    // Libraries restricts visible server libraries.
}

type providerSecret struct {
	Password string `json:"password,omitempty"` // Password authenticates a configured account.
	Token    string `json:"token,omitempty"`    // Token authenticates API requests.
}

type providerDocument struct {
	Version   int                       `json:"version"`   // Version identifies the persistent schema.
	Providers map[string]providerConfig `json:"providers"` // Providers contains non-secret settings by key.
}

type providerSecretsDocument struct {
	Version   int                       `json:"version"`   // Version identifies the persistent schema.
	Providers map[string]providerSecret `json:"providers"` // Providers contains owner-readable credentials.
}

type providerInfo struct {
	Key          string   `json:"key"`             // Key is the stable command and item identifier.
	Name         string   `json:"name"`            // Name is the provider display label.
	Configured   bool     `json:"configured"`      // Configured reports whether required settings exist.
	Capabilities []string `json:"capabilities"`    // Capabilities lists optional provider behavior.
	Error        string   `json:"error,omitempty"` // Error reports a validation failure.
}

type providerBrowseRequest struct {
	Kind   string `json:"kind,omitempty"`  // Kind selects playlists, albums, artists, or tracks.
	ID     string `json:"id,omitempty"`    // ID selects a parent collection.
	Offset int    `json:"offset,omitzero"` // Offset skips results for paging.
	Limit  int    `json:"limit,omitzero"`  // Limit caps returned results.
}

type providerPage struct {
	Provider string               `json:"provider"`          // Provider identifies the result source.
	Title    string               `json:"title"`             // Title describes the page.
	Entries  []providerCollection `json:"entries,omitempty"` // Entries contains browsable collections.
	Items    []MediaItem          `json:"items"`             // Items contains playable results.
	Next     int                  `json:"next,omitzero"`     // Next is the following offset, or zero at the end.
}

type providerCollection struct {
	Provider string `json:"provider"`           // Provider identifies the owning catalog.
	Kind     string `json:"kind"`               // Kind selects the follow-up browse route.
	ID       string `json:"id"`                 // ID is the provider-stable collection identity.
	Title    string `json:"title"`              // Title is the primary display label.
	Subtitle string `json:"subtitle,omitempty"` // Subtitle carries artist or collection context.
	Artwork  string `json:"artwork,omitempty"`  // Artwork is an optional HTTP(S) image.
	Count    int    `json:"count,omitzero"`     // Count is the known child count.
}

type providerStream struct {
	URL     string            `json:"url"`               // URL is the resolved audio address.
	Headers map[string]string `json:"headers,omitempty"` // Headers contains required request headers.
}

type mediaProvider interface {
	key() string
	name() string
	capabilities() []string
	validate(context.Context) error
}

type providerSearcher interface {
	search(context.Context, string, int) ([]MediaItem, error)
}

type providerBrowsable interface {
	browse(context.Context, providerBrowseRequest) (providerPage, error)
}

type providerResolver interface {
	resolve(context.Context, MediaItem) (providerStream, error)
}

type providerItemLookup interface {
	lookup(context.Context, string) (MediaItem, error)
}

type providerProgressReporter interface {
	reportProgress(context.Context, MediaItem, time.Duration, time.Duration, string) error
}

type providerPlaybackProgressReporter interface {
	reportPlaybackProgress(context.Context, MediaItem, providerProgressUpdate) error
}

type providerProgressUpdate struct {
	item      MediaItem
	position  time.Duration
	duration  time.Duration
	state     string
	completed bool
	scrobble  bool
}

// providerProgressSequencer preserves the playback controller's submission
// order across seeks, replacements, and terminal lifecycle updates.
type providerProgressSequencer struct {
	mu     sync.Mutex
	tail   chan struct{}
	work   sync.WaitGroup
	closed bool
}

func (sequencer *providerProgressSequencer) submit(update providerProgressUpdate, after func(error)) <-chan error {
	result := make(chan error, 1)
	sequencer.mu.Lock()
	if sequencer.closed {
		sequencer.mu.Unlock()
		result <- errors.New("provider progress synchronization is closed")
		close(result)
		return result
	}
	previous := sequencer.tail
	done := make(chan struct{})
	sequencer.tail = done
	sequencer.work.Go(func() {
		if previous != nil {
			<-previous
		}
		err := reportProviderProgressUpdate(update)
		if after != nil {
			after(err)
		}
		result <- err
		close(result)
		close(done)
		sequencer.mu.Lock()
		if sequencer.tail == done {
			sequencer.tail = nil
		}
		sequencer.mu.Unlock()
	})
	sequencer.mu.Unlock()
	return result
}

func (sequencer *providerProgressSequencer) wait() {
	sequencer.mu.Lock()
	sequencer.closed = true
	sequencer.mu.Unlock()
	sequencer.work.Wait()
}

type providerFavoriteToggler interface {
	setFavorite(context.Context, MediaItem, bool) error
}

type providerArtworkLoader interface {
	loadArtwork(context.Context, MediaItem) (string, error)
}

type providerCookieSource interface {
	cookiesFrom() string
}

type providerRegistry struct {
	mu        sync.Mutex
	providers map[string]mediaProvider
	order     []string
	recent    map[string]MediaItem
}

type providerSearchBatch struct {
	provider string
	items    []MediaItem
	err      error
}

var providerRegistryState struct {
	sync.Mutex
	registry *providerRegistry
}

func providerConfigPath() string {
	if path := configPath(); path != "" {
		return filepath.Join(filepath.Dir(path), "providers.json")
	}
	return ""
}

func providerSecretsPath() string {
	if path := configPath(); path != "" {
		return filepath.Join(filepath.Dir(path), "provider-secrets.json")
	}
	return ""
}

func defaultProviderDocument() providerDocument {
	return providerDocument{Version: providerDocumentVersion, Providers: map[string]providerConfig{
		"youtube":    {Type: "youtube", Enabled: true},
		"ytmusic":    {Type: "ytmusic", Enabled: true},
		"soundcloud": {Type: "soundcloud", Enabled: true},
		"mixcloud":   {Type: "mixcloud", Enabled: true},
	}}
}

func loadProviderDocuments() (providerDocument, providerSecretsDocument, error) {
	document := defaultProviderDocument()
	if data, err := os.ReadFile(providerConfigPath()); err == nil {
		var saved providerDocument
		if err := json.Unmarshal(data, &saved); err != nil {
			return document, providerSecretsDocument{}, fmt.Errorf("read providers: %w", err)
		}
		if saved.Version != providerDocumentVersion {
			return document, providerSecretsDocument{}, fmt.Errorf("unsupported providers.json version %d", saved.Version)
		}
		for key, config := range saved.Providers {
			document.Providers[strings.ToLower(strings.TrimSpace(key))] = config
		}
	} else if !os.IsNotExist(err) {
		return document, providerSecretsDocument{}, err
	}
	secrets := providerSecretsDocument{Version: providerDocumentVersion, Providers: map[string]providerSecret{}}
	if data, err := os.ReadFile(providerSecretsPath()); err == nil {
		if err := json.Unmarshal(data, &secrets); err != nil {
			return document, secrets, fmt.Errorf("read provider secrets: %w", err)
		}
		if secrets.Version != providerDocumentVersion {
			return document, secrets, fmt.Errorf("unsupported provider-secrets.json version %d", secrets.Version)
		}
	} else if !os.IsNotExist(err) {
		return document, secrets, err
	}
	return document, secrets, nil
}

func saveProviderDocuments(document providerDocument, secrets providerSecretsDocument) error {
	if document.Providers == nil {
		document.Providers = map[string]providerConfig{}
	}
	if secrets.Providers == nil {
		secrets.Providers = map[string]providerSecret{}
	}
	document.Version, secrets.Version = providerDocumentVersion, providerDocumentVersion
	if err := writeSecureJSON(providerConfigPath(), document); err != nil {
		return err
	}
	return writeSecureJSON(providerSecretsPath(), secrets)
}

func writeSecureJSON(path string, value any) error {
	if path == "" {
		return errors.New("no config directory on this system")
	}
	data, err := json.Marshal(value, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".chill-provider-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func providers() (*providerRegistry, error) {
	providerRegistryState.Lock()
	defer providerRegistryState.Unlock()
	if providerRegistryState.registry != nil {
		return providerRegistryState.registry, nil
	}
	document, secrets, err := loadProviderDocuments()
	if err != nil {
		return nil, err
	}
	registry := &providerRegistry{providers: map[string]mediaProvider{}, recent: map[string]MediaItem{}}
	keys := slices.Sorted(maps.Keys(document.Providers))
	for _, key := range keys {
		config := document.Providers[key]
		if !config.Enabled {
			continue
		}
		if config.Type == "" {
			config.Type = key
		}
		var provider mediaProvider
		switch strings.ToLower(config.Type) {
		case "youtube", "ytmusic", "soundcloud", "mixcloud":
			provider = newYTDLPProvider(key, config)
		case "navidrome", "subsonic", "jellyfin", "emby", "plex", "audiobookshelf", "spotify", "tidal", "qobuz":
			provider = newHTTPMediaProvider(key, config, secrets.Providers[key])
		default:
			continue
		}
		registry.providers[key] = provider
		registry.order = append(registry.order, key)
	}
	providerRegistryState.registry = registry
	return registry, nil
}

func resetProviders() {
	providerRegistryState.Lock()
	providerRegistryState.registry = nil
	providerRegistryState.Unlock()
}

func (registry *providerRegistry) list(ctx context.Context, validate bool) []providerInfo {
	infos := make([]providerInfo, len(registry.order))
	var wait sync.WaitGroup
	for index, key := range registry.order {
		provider := registry.providers[key]
		infos[index] = providerInfo{Key: key, Name: provider.name(), Configured: true, Capabilities: provider.capabilities()}
		if validate {
			wait.Go(func() {
				if err := provider.validate(ctx); err != nil {
					infos[index].Configured, infos[index].Error = false, err.Error()
				}
			})
		}
	}
	if validate {
		wait.Wait()
	}
	return infos
}

func (registry *providerRegistry) search(ctx context.Context, providerKey, query string, limit int, progress func(string, int, int)) ([]MediaItem, error) {
	results, total, err := registry.searchStream(ctx, providerKey, query, limit)
	if err != nil {
		return nil, err
	}
	byProvider := map[string][]MediaItem{}
	var errs []error
	completed := 0
	for result := range results {
		completed++
		if progress != nil {
			progress(result.provider, completed, total)
		}
		if result.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", result.provider, result.err))
			continue
		}
		byProvider[result.provider] = result.items
	}
	seen := map[string]bool{}
	var combined []MediaItem
	for _, key := range registry.order {
		for _, item := range byProvider[key] {
			identity := providerSearchIdentity(item)
			if !seen[identity] {
				seen[identity] = true
				combined = append(combined, item)
			}
		}
	}
	if len(combined) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return combined, nil
}

func providerSearchIdentity(item MediaItem) string {
	compact := func(value string) string { return strings.Join(strings.Fields(strings.ToLower(value)), " ") }
	title, artist := compact(item.Title), compact(item.Artist)
	if title != "" && artist != "" {
		return "track\x00" + artist + "\x00" + title
	}
	return item.Provider + "\x00" + item.ProviderID
}

func (registry *providerRegistry) searchStream(ctx context.Context, providerKey, query string, limit int) (<-chan providerSearchBatch, int, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, 0, errors.New("search query is required")
	}
	limit = min(100, max(1, limit))
	keys := registry.order
	if providerKey != "" && providerKey != "all" {
		key := strings.ToLower(providerKey)
		if registry.providers[key] == nil {
			return nil, 0, fmt.Errorf("unknown provider %q", providerKey)
		}
		keys = []string{key}
	}
	searchable := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := registry.providers[key].(providerSearcher); ok {
			searchable = append(searchable, key)
		}
	}
	results := make(chan providerSearchBatch, len(searchable))
	var wait sync.WaitGroup
	for _, key := range searchable {
		searcher := registry.providers[key].(providerSearcher)
		wait.Go(func() {
			items, err := searcher.search(ctx, query, limit)
			if err == nil {
				registry.remember(items)
			}
			results <- providerSearchBatch{provider: key, items: items, err: err}
		})
	}
	go func() {
		wait.Wait()
		close(results)
	}()
	return results, len(searchable), nil
}

func (registry *providerRegistry) browse(ctx context.Context, key string, request providerBrowseRequest) (providerPage, error) {
	provider := registry.providers[strings.ToLower(key)]
	if provider == nil {
		return providerPage{}, fmt.Errorf("unknown provider %q", key)
	}
	browser, ok := provider.(providerBrowsable)
	if !ok {
		return providerPage{}, fmt.Errorf("provider %s does not support browsing", key)
	}
	request.Limit = min(100, max(1, request.Limit))
	page, err := browser.browse(ctx, request)
	if err == nil {
		registry.remember(page.Items)
	}
	return page, err
}

func (registry *providerRegistry) remember(items []MediaItem) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	for _, item := range items {
		registry.recent[item.Provider+"\x00"+item.ProviderID] = item
	}
	if len(registry.recent) <= 1000 {
		return
	}
	// Search results are a convenience cache; deterministic pruning is enough.
	keys := slices.Sorted(maps.Keys(registry.recent))
	for _, key := range keys[:len(keys)-1000] {
		delete(registry.recent, key)
	}
}

func (registry *providerRegistry) item(provider, id string) (MediaItem, bool) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	item, ok := registry.recent[strings.ToLower(provider)+"\x00"+id]
	return item, ok
}

func (registry *providerRegistry) lookup(ctx context.Context, provider, id string) (MediaItem, error) {
	if item, ok := registry.item(provider, id); ok {
		return item, nil
	}
	return registry.fetch(ctx, provider, id)
}

func (registry *providerRegistry) fetch(ctx context.Context, provider, id string) (MediaItem, error) {
	source := registry.providers[strings.ToLower(provider)]
	if source == nil {
		return MediaItem{}, fmt.Errorf("provider %q is not configured", provider)
	}
	lookup, ok := source.(providerItemLookup)
	if !ok {
		return MediaItem{}, errors.New("provider item needs complete metadata from search or browse")
	}
	item, err := lookup.lookup(ctx, id)
	if err != nil {
		return MediaItem{}, err
	}
	registry.remember([]MediaItem{item})
	return item, nil
}

func (registry *providerRegistry) resolve(ctx context.Context, item MediaItem) (providerStream, error) {
	provider := registry.providers[item.Provider]
	if provider == nil {
		return providerStream{}, fmt.Errorf("provider %q is not configured", item.Provider)
	}
	resolver, ok := provider.(providerResolver)
	if !ok {
		return providerStream{}, fmt.Errorf("provider %s cannot resolve playback", item.Provider)
	}
	return resolver.resolve(ctx, item)
}

func reportProviderProgressUpdate(progress providerProgressUpdate) error {
	item := progress.item
	if item.Kind != MediaProvider || item.Provider == "" {
		return nil
	}
	registry, err := providers()
	if err != nil {
		return err
	}
	provider := registry.providers[item.Provider]
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if reporter, ok := provider.(providerPlaybackProgressReporter); ok {
		return reporter.reportPlaybackProgress(ctx, item, progress)
	}
	if reporter, ok := provider.(providerProgressReporter); ok {
		return reporter.reportProgress(ctx, item, progress.position, progress.duration, progress.state)
	}
	return nil
}

func setProviderFavorite(item MediaItem, favorite bool) error {
	if item.Kind != MediaProvider || item.Provider == "" {
		return nil
	}
	registry, err := providers()
	if err != nil {
		return err
	}
	provider := registry.providers[item.Provider]
	toggler, ok := provider.(providerFavoriteToggler)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return toggler.setFavorite(ctx, item, favorite)
}

func providerSource(key, id string) string {
	return (&url.URL{Scheme: "chill-provider", Host: key, Path: "/" + id}).String()
}

func providerMediaItem(key, id, title, artist, album, artwork, source string, duration float64) MediaItem {
	meta := map[string]string{}
	if source != "" {
		meta["url"] = source
	}
	item := MediaItem{Kind: MediaProvider, Provider: key, ProviderID: id, Source: providerSource(key, id),
		Title: cleanProviderText(title), Artist: cleanProviderText(artist), Album: cleanProviderText(album), Artwork: artwork, Duration: duration, ProviderMeta: meta, AddedAt: time.Now().UTC()}
	item.ID = mediaID(item.Kind, key+"\x00"+id)
	return item
}

func cleanProviderText(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func resolveProviderAudio(ctx context.Context, source string) (resolvedAudio, error) {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "chill-provider" || u.Host == "" {
		return resolvedAudio{}, errors.New("invalid provider source")
	}
	id := strings.TrimPrefix(u.Path, "/")
	registry, err := providers()
	if err != nil {
		return resolvedAudio{}, err
	}
	item, ok := registry.item(u.Host, id)
	if !ok {
		item = providerMediaItem(u.Host, id, id, "", "", "", "", 0)
	}
	stream, err := registry.resolve(ctx, item)
	if err != nil {
		if complete, lookupErr := registry.fetch(ctx, item.Provider, item.ProviderID); lookupErr == nil {
			item = complete
			stream, err = registry.resolve(ctx, item)
		}
	}
	if err != nil {
		return resolvedAudio{}, err
	}
	artwork := item.Artwork
	if artwork == "" {
		if loader, ok := registry.providers[item.Provider].(providerArtworkLoader); ok {
			if loaded, loadErr := loader.loadArtwork(ctx, item); loadErr == nil {
				artwork = loaded
				item.Artwork = loaded
				registry.remember([]MediaItem{item})
			}
		}
	}
	if stream.URL == "" {
		return resolvedAudio{}, errors.New("provider returned no playback URL")
	}
	// Catalog providers may resolve to a public media page. Run those pages
	// through the regular extractor path before handing them to FFmpeg.
	if sourceNeedsYtdl(stream.URL) {
		cookiesFrom := ""
		if source, ok := registry.providers[item.Provider].(providerCookieSource); ok {
			cookiesFrom = source.cookiesFrom()
		}
		resolved, err := resolveAudioWithCookies(ctx, stream.URL, cookiesFrom)
		if err != nil {
			return resolvedAudio{}, err
		}
		if len(stream.Headers) > 0 {
			if resolved.Headers == nil {
				resolved.Headers = map[string]string{}
			}
			for key, value := range stream.Headers {
				resolved.Headers[key] = value
			}
		}
		resolved.Artwork = artwork
		return resolved, nil
	}
	return resolvedAudio{URL: stream.URL, Headers: stream.Headers, Artwork: artwork}, nil
}

type ytDLPProvider struct {
	providerKey string
	displayName string
	prefix      string
	config      providerConfig
	client      *http.Client
	apiURL      string
}

func newYTDLPProvider(key string, config providerConfig) *ytDLPProvider {
	provider := &ytDLPProvider{providerKey: key, config: config, client: providerHTTPClient(), apiURL: mixcloudAPIURL}
	switch strings.ToLower(config.Type) {
	case "youtube":
		provider.displayName, provider.prefix = "YouTube", "ytsearch"
	case "ytmusic":
		provider.displayName, provider.prefix = "YouTube Music", "ytsearch"
	case "soundcloud":
		provider.displayName, provider.prefix = "SoundCloud", "scsearch"
	case "mixcloud":
		provider.displayName = "Mixcloud"
	}
	return provider
}

func (provider *ytDLPProvider) key() string         { return provider.providerKey }
func (provider *ytDLPProvider) name() string        { return provider.displayName }
func (provider *ytDLPProvider) cookiesFrom() string { return provider.config.CookiesFrom }
func (provider *ytDLPProvider) capabilities() []string {
	return []string{"search", "playback", "artwork", "lyrics"}
}
func (provider *ytDLPProvider) validate(ctx context.Context) error {
	if findYtdl("") == "" {
		return errors.New("yt-dlp is not installed")
	}
	if provider.providerKey == "mixcloud" {
		_, _, err := provider.searchMixcloud(ctx, "music", 1, 0)
		return err
	}
	return nil
}

func (provider *ytDLPProvider) search(ctx context.Context, query string, limit int) ([]MediaItem, error) {
	if provider.providerKey == "mixcloud" {
		items, _, err := provider.searchMixcloud(ctx, query, limit, 0)
		return items, err
	}
	extractor := findYtdl("")
	if extractor == "" {
		return nil, errors.New("yt-dlp is not installed")
	}
	search := fmt.Sprintf("%s%d:%s", provider.prefix, limit, query)
	args, err := extractorArgs()
	if err != nil {
		return nil, err
	}
	args = append(args, "--flat-playlist", "--skip-download", "--dump-json")
	if provider.config.CookiesFrom != "" {
		args = append(args, "--cookies-from-browser", provider.config.CookiesFrom)
	}
	args = append(args, "--", search)
	command := exec.CommandContext(ctx, extractor, args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var diagnostics tailBuffer
	command.Stderr = &diagnostics
	command.WaitDelay = time.Second
	tree, startErr := startInTree(command)
	if startErr != nil {
		return nil, startErr
	}
	stopTree := context.AfterFunc(ctx, func() { tree.kill() })
	defer stopTree()
	defer tree.kill()
	var items []MediaItem
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		var entry struct {
			ID         string  `json:"id"`
			URL        string  `json:"url"`
			WebpageURL string  `json:"webpage_url"`
			Title      string  `json:"title"`
			Uploader   string  `json:"uploader"`
			Artist     string  `json:"artist"`
			Album      string  `json:"album"`
			Thumbnail  string  `json:"thumbnail"`
			Duration   float64 `json:"duration"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.ID == "" {
			continue
		}
		pageURL := firstNonempty(entry.WebpageURL, entry.URL)
		switch provider.providerKey {
		case "youtube":
			if !strings.HasPrefix(pageURL, "http") {
				pageURL = "https://www.youtube.com/watch?v=" + entry.ID
			}
		case "ytmusic":
			pageURL = "https://music.youtube.com/watch?v=" + entry.ID
		}
		if !strings.HasPrefix(pageURL, "http://") && !strings.HasPrefix(pageURL, "https://") {
			continue
		}
		artist := firstNonempty(entry.Artist, entry.Uploader)
		items = append(items, providerMediaItem(provider.providerKey, entry.ID, entry.Title, artist, entry.Album, entry.Thumbnail, pageURL, entry.Duration))
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		tree.kill()
		stdout.Close()
	}
	waitErr := command.Wait()
	if scanErr != nil {
		return nil, scanErr
	}
	if waitErr != nil && len(items) == 0 {
		return nil, fmt.Errorf("search failed: %w: %s", waitErr, strings.TrimSpace(diagnostics.String()))
	}
	return items, nil
}

func (provider *ytDLPProvider) browse(ctx context.Context, request providerBrowseRequest) (providerPage, error) {
	query := strings.TrimSpace(request.ID)
	if query == "" {
		query = "music"
	}
	if provider.providerKey == "mixcloud" {
		items, next, err := provider.searchMixcloud(ctx, query, request.Limit, request.Offset)
		return providerPage{Provider: provider.key(), Title: provider.name(), Items: items, Next: next}, err
	}
	items, err := provider.search(ctx, query, request.Limit)
	return providerPage{Provider: provider.key(), Title: provider.name(), Items: items}, err
}

func (provider *ytDLPProvider) lookup(ctx context.Context, id string) (MediaItem, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 512 || strings.ContainsAny(id, "\x00\r\n") {
		return MediaItem{}, errors.New("invalid provider item id")
	}
	if provider.providerKey == "mixcloud" {
		return provider.lookupMixcloud(ctx, id)
	}
	var target string
	switch provider.providerKey {
	case "youtube":
		target = "https://www.youtube.com/watch?v=" + url.QueryEscape(id)
	case "ytmusic":
		target = "https://music.youtube.com/watch?v=" + url.QueryEscape(id)
	default:
		return MediaItem{}, errors.New("provider item needs a page URL from search or browse")
	}
	return providerMediaItem(provider.providerKey, id, id, "", "", "", target, 0), nil
}

func (provider *ytDLPProvider) resolve(_ context.Context, item MediaItem) (providerStream, error) {
	streamURL := item.ProviderMeta["url"]
	if streamURL == "" {
		switch provider.providerKey {
		case "youtube":
			streamURL = "https://www.youtube.com/watch?v=" + item.ProviderID
		case "ytmusic":
			streamURL = "https://music.youtube.com/watch?v=" + item.ProviderID
		case "mixcloud":
			if id, ok := normalizeMixcloudID(item.ProviderID); ok {
				streamURL = mixcloudPageURL(id)
			}
		}
	}
	if streamURL == "" {
		return providerStream{}, errors.New("provider result no longer has a playback URL; search again")
	}
	return providerStream{URL: streamURL}, nil
}

type mixcloudEntry struct {
	Key         string  `json:"key"`          // Key is the stable show path.
	Name        string  `json:"name"`         // Name is the show title.
	AudioLength float64 `json:"audio_length"` // AudioLength is the duration in seconds.
	Pictures    struct {
		ExtraLarge string `json:"extra_large"` // ExtraLarge is the preferred artwork URL.
		Large      string `json:"large"`       // Large is the large artwork URL.
		Medium     string `json:"medium"`      // Medium is the medium artwork URL.
	} `json:"pictures"` // Pictures contains show artwork sizes.
	User struct {
		Name     string `json:"name"`     // Name is the uploader display name.
		Username string `json:"username"` // Username is the uploader account name.
	} `json:"user"` // User identifies the show uploader.
}

type mixcloudSearchResponse struct {
	Data   []mixcloudEntry `json:"data"` // Data contains matching shows.
	Paging struct {
		Next string `json:"next"` // Next is the next-page API URL when more results exist.
	} `json:"paging"` // Paging describes the next result page.
}

func (provider *ytDLPProvider) searchMixcloud(ctx context.Context, query string, limit, offset int) ([]MediaItem, int, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, 0, errors.New("search query is required")
	}
	limit = min(100, max(1, limit))
	offset = max(0, offset)
	values := url.Values{
		"q":      {query},
		"type":   {"cloudcast"},
		"limit":  {strconv.Itoa(limit)},
		"offset": {strconv.Itoa(offset)},
	}
	var response mixcloudSearchResponse
	if err := provider.mixcloudJSON(ctx, "search/", values, &response); err != nil {
		return nil, 0, err
	}
	items := make([]MediaItem, 0, len(response.Data))
	for _, entry := range response.Data {
		if item, ok := provider.mixcloudItem(entry); ok {
			items = append(items, item)
		}
	}
	next := 0
	if response.Paging.Next != "" && len(response.Data) > 0 {
		next = offset + len(response.Data)
	}
	return items, next, nil
}

func (provider *ytDLPProvider) lookupMixcloud(ctx context.Context, id string) (MediaItem, error) {
	id, ok := normalizeMixcloudID(id)
	if !ok {
		return MediaItem{}, errors.New("invalid Mixcloud item id")
	}
	var entry mixcloudEntry
	if err := provider.mixcloudJSON(ctx, id+"/", nil, &entry); err != nil {
		return MediaItem{}, err
	}
	item, ok := provider.mixcloudItem(entry)
	if !ok {
		return MediaItem{}, errors.New("mixcloud item was not found")
	}
	return item, nil
}

func (provider *ytDLPProvider) mixcloudJSON(ctx context.Context, path string, query url.Values, output any) error {
	base, err := url.Parse(provider.apiURL)
	if err != nil || base.Scheme != "https" && base.Scheme != "http" || base.Host == "" {
		return errors.New("invalid Mixcloud API URL")
	}
	target := base.ResolveReference(&url.URL{Path: strings.TrimPrefix(path, "/")})
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	response, err := provider.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("mixcloud returned %s: %s", response.Status, cleanProviderText(string(body)))
	}
	return json.UnmarshalRead(io.LimitReader(response.Body, providerResponseLimit), output)
}

func (provider *ytDLPProvider) mixcloudItem(entry mixcloudEntry) (MediaItem, bool) {
	id, ok := normalizeMixcloudID(entry.Key)
	if !ok || strings.TrimSpace(entry.Name) == "" {
		return MediaItem{}, false
	}
	artist := firstNonempty(entry.User.Name, entry.User.Username)
	artwork := firstNonempty(entry.Pictures.ExtraLarge, entry.Pictures.Large, entry.Pictures.Medium)
	return providerMediaItem(provider.key(), id, entry.Name, artist, "", artwork, mixcloudPageURL(id), entry.AudioLength), true
}

func normalizeMixcloudID(value string) (string, bool) {
	value = strings.Trim(strings.TrimSpace(value), "/")
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return "", false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 255 || strings.ContainsAny(part, "\x00\r\n\\") {
			return "", false
		}
	}
	return strings.Join(parts, "/"), true
}

func mixcloudPageURL(id string) string {
	return (&url.URL{Scheme: "https", Host: "www.mixcloud.com", Path: "/" + id + "/"}).String()
}

func providerHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if len(via) == 0 {
				return nil
			}
			origin := via[0].URL
			previous := via[len(via)-1].URL
			if (origin.Scheme == "https" || previous.Scheme == "https") && request.URL.Scheme != "https" {
				return errors.New("refusing provider redirect from HTTPS to an insecure destination")
			}
			// net/http rebuilds every redirected request from the original
			// headers. Compare against the authenticated origin on every hop so
			// a second redirect on a foreign host cannot restore credentials.
			if !sameURLOrigin(origin, request.URL) {
				for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization", "X-Emby-Token", "X-Plex-Token", "X-User-Auth-Token", "X-App-Id"} {
					request.Header.Del(header)
				}
			}
			return nil
		},
	}
}

func sameURLOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && effectiveURLPort(a) == effectiveURLPort(b)
}

func effectiveURLPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(value.Scheme, "http") {
		return "80"
	}
	return ""
}
