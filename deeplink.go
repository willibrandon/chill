package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const maxDeepLinkLength = 8192

var providerKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func isDeepLinkInput(raw string) bool {
	return strings.HasPrefix(strings.ToLower(raw), "chill://")
}

type deepLink struct {
	action   string
	item     MediaItem
	provider string
	kind     string
	value    string
	next     bool
}

func parseDeepLink(raw string) (deepLink, error) {
	if len(raw) == 0 || len(raw) > maxDeepLinkLength || strings.ContainsAny(raw, "\x00\r\n") {
		return deepLink{}, errors.New("invalid chill link length or characters")
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "chill") || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return deepLink{}, errors.New("invalid chill link")
	}
	action := strings.ToLower(u.Host)
	if (action != "play" && action != "queue") || u.Path != "" && u.Path != "/" {
		return deepLink{}, errors.New("chill link must target play or queue")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return deepLink{}, errors.New("invalid chill link query")
	}
	allowed := map[string]bool{"target": true, "provider": true, "id": true, "q": true, "album": true, "playlist": true, "title": true, "artist": true, "artwork": true, "next": true}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			return deepLink{}, fmt.Errorf("unsupported or repeated chill link parameter %q", key)
		}
		if len(values[0]) > 4096 || strings.ContainsAny(values[0], "\x00\r\n") {
			return deepLink{}, fmt.Errorf("invalid chill link parameter %q", key)
		}
	}
	provider, target := strings.ToLower(query.Get("provider")), query.Get("target")
	var item MediaItem
	kind, value := "", ""
	if provider == "" {
		if query.Get("id") != "" || query.Get("q") != "" || query.Get("album") != "" || query.Get("playlist") != "" || target == "" {
			return deepLink{}, errors.New("chill link needs target or provider and id")
		}
		item, err = itemFromURL(target)
	} else {
		selectors := []struct{ kind, value string }{{"id", query.Get("id")}, {"query", query.Get("q")}, {"album", query.Get("album")}, {"playlist", query.Get("playlist")}}
		for _, selector := range selectors {
			if selector.value == "" {
				continue
			}
			if kind != "" {
				return deepLink{}, errors.New("provider links need exactly one of id, q, album, or playlist")
			}
			kind, value = selector.kind, selector.value
		}
		if kind == "" {
			return deepLink{}, errors.New("provider links need exactly one of id, q, album, or playlist")
		}
		if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return deepLink{}, errors.New("invalid provider selector")
		}
		if kind == "id" {
			item, err = providerDeepLinkItem(provider, value, target, query)
		} else {
			if target != "" || query.Get("title") != "" || query.Get("artist") != "" || query.Get("artwork") != "" {
				return deepLink{}, errors.New("provider collection links do not accept item metadata")
			}
			err = validateDeepLinkProvider(provider)
		}
	}
	if err != nil {
		return deepLink{}, err
	}
	next := false
	if rawNext := query.Get("next"); rawNext != "" {
		if action != "queue" || rawNext != "true" && rawNext != "false" {
			return deepLink{}, errors.New("next must be true or false on a queue link")
		}
		next = rawNext == "true"
	}
	return deepLink{action: action, item: item, provider: provider, kind: kind, value: value, next: next}, nil
}

func providerDeepLinkItem(provider, id, target string, query url.Values) (MediaItem, error) {
	if !providerKeyPattern.MatchString(provider) || id == "" || len(id) > 512 || strings.ContainsAny(id, "\x00\r\n") {
		return MediaItem{}, errors.New("invalid provider identity")
	}
	if err := validateDeepLinkProvider(provider); err != nil {
		return MediaItem{}, err
	}
	registry, _ := providers()
	if target != "" {
		if err := validateProviderTarget(provider, target); err != nil {
			return MediaItem{}, err
		}
	}
	artwork := query.Get("artwork")
	if artwork != "" {
		if _, err := strictHTTPURL(artwork); err != nil {
			return MediaItem{}, fmt.Errorf("invalid artwork: %w", err)
		}
	}
	item := providerMediaItem(provider, id, firstNonempty(query.Get("title"), id), query.Get("artist"), query.Get("album"), artwork, target, 0)
	registry.remember([]MediaItem{item})
	return item, nil
}

func validateDeepLinkProvider(provider string) error {
	if !providerKeyPattern.MatchString(provider) {
		return errors.New("invalid provider identity")
	}
	registry, err := providers()
	if err != nil {
		return err
	}
	if registry.providers[provider] == nil {
		return fmt.Errorf("provider %q is not enabled", provider)
	}
	return nil
}

func validateProviderTarget(provider, target string) error {
	u, err := strictHTTPURL(target)
	if err != nil {
		return fmt.Errorf("invalid provider target: %w", err)
	}
	allowedHosts := map[string][]string{
		"youtube": {"youtube.com", "youtu.be"}, "ytmusic": {"music.youtube.com", "youtube.com", "youtu.be"},
		"soundcloud": {"soundcloud.com"}, "mixcloud": {"mixcloud.com"},
	}
	hosts, restricted := allowedHosts[provider]
	if !restricted {
		return nil
	}
	for _, host := range hosts {
		if u.Hostname() == host || strings.HasSuffix(u.Hostname(), "."+host) {
			return nil
		}
	}
	return fmt.Errorf("target host is not valid for %s", provider)
}

func strictHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("target must be an absolute HTTP(S) URL without credentials")
	}
	return u, nil
}

func runDeepLink(ctx context.Context, raw string, foreground bool) (string, error) {
	link, err := parseDeepLink(raw)
	if err != nil {
		return "", err
	}
	items, err := resolveDeepLinkItems(ctx, link)
	if err != nil {
		return "", err
	}
	playable := items[:0]
	for _, item := range items {
		if providerPlaybackAvailable(item) == nil {
			playable = append(playable, item)
		}
	}
	items = playable
	if len(items) == 0 {
		return "", errors.New("link contains no playable tracks or previews")
	}
	if foreground {
		if link.action != "play" {
			return "", errors.New("foreground mode only supports chill://play links")
		}
		return "", runForegroundMediaItems(items)
	}
	if link.action == "play" {
		return playMediaItems(items)
	}
	action := "queue-append"
	if link.next {
		action = "queue-next"
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
		return sendItems(action, items)
	}
}

func resolveDeepLinkItems(ctx context.Context, link deepLink) ([]MediaItem, error) {
	if link.kind == "" || link.kind == "id" {
		return []MediaItem{link.item}, nil
	}
	registry, err := providers()
	if err != nil {
		return nil, err
	}
	if link.kind == "query" {
		items, err := registry.search(ctx, link.provider, link.value, 20, nil)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, errors.New("provider search returned no playable results")
		}
		return items, nil
	}
	items := make([]MediaItem, 0, 100)
	offset := 0
	for len(items) < 5000 {
		page, err := registry.browse(ctx, link.provider, providerBrowseRequest{Kind: link.kind, ID: link.value, Offset: offset, Limit: min(100, 5000-len(items))})
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if page.Next <= offset {
			break
		}
		offset = page.Next
	}
	if len(items) == 0 {
		return nil, errors.New("provider collection contains no playable items")
	}
	return items, nil
}

func runLinkCommand(ctx context.Context, args []string, foreground bool) (string, error) {
	if helpRequested(args) {
		return `Usage: chill link <command>

Commands:
  open <chill://play?...>   validate and open a link
  register                  register chill:// links for this user
  unregister                remove the per-user link registration
  status                    show registration status`, nil
	}
	if len(args) == 0 {
		return "", errors.New("usage: chill link <open URI|register|unregister|status>")
	}
	switch args[0] {
	case "open":
		if len(args) != 2 {
			return "", errors.New("usage: chill link open <chill://play?...>")
		}
		return runDeepLink(ctx, args[1], foreground)
	case "register":
		if len(args) != 1 {
			return "", errors.New("usage: chill link register")
		}
		return registerDeepLinks()
	case "unregister":
		if len(args) != 1 {
			return "", errors.New("usage: chill link unregister")
		}
		return unregisterDeepLinks()
	case "status":
		if len(args) != 1 {
			return "", errors.New("usage: chill link status")
		}
		registered, detail := deepLinkRegistrationStatus()
		if !registered {
			return "not registered: " + detail, nil
		}
		return "registered: " + detail, nil
	default:
		return "", fmt.Errorf("unknown link command %q", args[0])
	}
}
