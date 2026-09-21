package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const providerResponseLimit = 32 << 20

var writeProviderArtwork = func(file *os.File, data []byte) (int, error) { return file.Write(data) }

type httpMediaProvider struct {
	providerKey string
	kind        string
	displayName string
	config      providerConfig
	secret      providerSecret
	client      *http.Client
}

func newHTTPMediaProvider(key string, config providerConfig, secret providerSecret) *httpMediaProvider {
	kind := strings.ToLower(config.Type)
	names := map[string]string{
		"navidrome": "Navidrome", "subsonic": "Subsonic", "jellyfin": "Jellyfin", "emby": "Emby",
		"plex": "Plex", "audiobookshelf": "Audiobookshelf", "spotify": "Spotify", "tidal": "Tidal", "qobuz": "Qobuz",
	}
	if config.URL == "" {
		config.URL = map[string]string{
			"spotify": "https://api.spotify.com",
			"tidal":   "https://openapi.tidal.com",
			"qobuz":   "https://www.qobuz.com/api.json/0.2",
		}[kind]
	}
	return &httpMediaProvider{providerKey: key, kind: kind, displayName: names[kind], config: config, secret: secret, client: providerHTTPClient()}
}

func (provider *httpMediaProvider) key() string  { return provider.providerKey }
func (provider *httpMediaProvider) name() string { return provider.displayName }
func (provider *httpMediaProvider) capabilities() []string {
	capabilities := []string{"search", "browse", "artwork", "authentication"}
	if provider.kind == "spotify" || provider.kind == "tidal" || provider.kind == "qobuz" {
		capabilities = append(capabilities, "catalog", "previews")
	} else {
		capabilities = append(capabilities, "playback")
	}
	switch provider.kind {
	case "navidrome", "subsonic", "jellyfin", "emby", "plex", "spotify":
		capabilities = append(capabilities, "playlists")
	}
	switch provider.kind {
	case "navidrome", "subsonic", "jellyfin", "emby", "plex", "audiobookshelf":
		capabilities = append(capabilities, "progress")
	}
	if provider.kind == "navidrome" || provider.kind == "subsonic" {
		capabilities = append(capabilities, "scrobble")
	}
	switch provider.kind {
	case "navidrome", "subsonic", "jellyfin", "emby", "spotify", "tidal", "qobuz":
		capabilities = append(capabilities, "favorites")
	}
	return capabilities
}

func (provider *httpMediaProvider) validate(ctx context.Context) error {
	if provider.config.URL == "" {
		return errors.New("server URL is required")
	}
	u, err := url.Parse(provider.config.URL)
	if err != nil || u.User != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("provider URL must be absolute HTTP(S)")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		// Local servers commonly use HTTP, but make remote clear-text credentials explicit.
		return errors.New("remote provider URLs must use HTTPS")
	}
	switch provider.kind {
	case "navidrome", "subsonic":
		if provider.config.Username == "" || provider.secret.Password == "" && provider.secret.Token == "" {
			return errors.New("username and password or token are required")
		}
		var response map[string]any
		return provider.subsonicJSON(ctx, "ping.view", nil, &response)
	case "jellyfin", "emby", "plex", "audiobookshelf", "spotify", "tidal", "qobuz":
		if provider.secret.Token == "" {
			return errors.New("an account access token is required")
		}
		if provider.kind == "qobuz" && provider.config.ClientID == "" {
			return errors.New("a Qobuz app id is required")
		}
		switch provider.kind {
		case "jellyfin", "emby":
			var output map[string]any
			return provider.getJSON(ctx, "/System/Info", nil, &output)
		case "audiobookshelf":
			var output map[string]any
			return provider.getJSON(ctx, "/api/me", nil, &output)
		case "spotify":
			var output map[string]any
			return provider.getJSON(ctx, "/v1/search", url.Values{"q": {"chill"}, "type": {"track"}, "limit": {"1"}}, &output)
		default:
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(provider.config.URL, "/"), nil)
			if err != nil {
				return err
			}
			provider.authorize(request)
			response, err := provider.client.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				return fmt.Errorf("server returned %s", response.Status)
			}
		}
	}
	return nil
}

func (provider *httpMediaProvider) search(ctx context.Context, query string, limit int) ([]MediaItem, error) {
	switch provider.kind {
	case "navidrome", "subsonic":
		return provider.searchSubsonic(ctx, query, limit)
	case "jellyfin", "emby":
		return provider.searchEmby(ctx, query, limit)
	case "plex":
		return provider.searchPlex(ctx, query, limit)
	case "audiobookshelf":
		return provider.searchAudiobookshelf(ctx, query, limit)
	case "spotify":
		return provider.searchSpotify(ctx, query, limit)
	case "tidal", "qobuz":
		return provider.searchOpenCatalog(ctx, query, limit)
	default:
		return nil, fmt.Errorf("provider %s does not support search", provider.kind)
	}
}

func (provider *httpMediaProvider) lookup(ctx context.Context, id string) (MediaItem, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 512 || strings.ContainsAny(id, "\x00\r\n") {
		return MediaItem{}, errors.New("invalid provider item id")
	}
	switch provider.kind {
	case "navidrome", "subsonic":
		var root map[string]any
		if err := provider.subsonicJSON(ctx, "getSong.view", url.Values{"id": {id}}, &root); err != nil {
			return MediaItem{}, err
		}
		if item, ok := provider.subsonicItem(mapValue(root["song"])); ok {
			return item, nil
		}
	case "jellyfin", "emby":
		path := "/Items/" + url.PathEscape(id)
		if provider.config.UserID != "" {
			path = "/Users/" + url.PathEscape(provider.config.UserID) + "/Items/" + url.PathEscape(id)
		}
		var entry map[string]any
		if err := provider.getJSON(ctx, path, url.Values{"Fields": {"Genres,MediaSources,Path"}}, &entry); err != nil {
			return MediaItem{}, err
		}
		if stringValue(entry["Id"]) != "" {
			return provider.embyItem(entry), nil
		}
	case "plex":
		tracks, err := provider.plexTracks(ctx, "/library/metadata/"+url.PathEscape(id), nil)
		if err != nil {
			return MediaItem{}, err
		}
		if len(tracks) > 0 {
			return provider.plexItem(tracks[0]), nil
		}
	case "audiobookshelf":
		itemID, _, _ := strings.Cut(id, ":")
		items, err := provider.audiobookshelfTracks(ctx, map[string]any{"id": itemID})
		if err != nil {
			return MediaItem{}, err
		}
		for _, item := range items {
			if item.ProviderID == id {
				return item, nil
			}
		}
	case "spotify":
		var track map[string]any
		if err := provider.getJSON(ctx, "/v1/tracks/"+url.PathEscape(id), nil, &track); err != nil {
			return MediaItem{}, err
		}
		if stringValue(track["id"]) != "" {
			return provider.spotifyItem(track), nil
		}
	case "tidal", "qobuz":
		path, values := "/tracks/"+url.PathEscape(id), url.Values(nil)
		if provider.kind == "qobuz" {
			path, values = "/track/get", url.Values{"track_id": {id}, "app_id": {provider.config.ClientID}}
		}
		var track map[string]any
		if err := provider.getJSON(ctx, path, values, &track); err != nil {
			return MediaItem{}, err
		}
		if item, ok := provider.openCatalogItem(track); ok {
			return item, nil
		}
	}
	return MediaItem{}, errors.New("provider item was not found")
}

func (provider *httpMediaProvider) browse(ctx context.Context, request providerBrowseRequest) (providerPage, error) {
	switch provider.kind {
	case "navidrome", "subsonic":
		return provider.browseSubsonic(ctx, request)
	case "jellyfin", "emby":
		return provider.browseEmby(ctx, request)
	case "plex":
		return provider.browsePlex(ctx, request)
	case "spotify":
		return provider.browseSpotify(ctx, request)
	case "audiobookshelf":
		return provider.browseAudiobookshelf(ctx, request)
	default:
		query := firstNonempty(request.ID, request.Kind, "music")
		items, err := provider.search(ctx, query, request.Limit)
		return providerPage{Provider: provider.key(), Title: provider.name(), Items: items}, err
	}
}

func (provider *httpMediaProvider) browseSubsonic(ctx context.Context, request providerBrowseRequest) (providerPage, error) {
	kind := strings.ToLower(request.Kind)
	if kind == "" || kind == "home" || kind == "music" {
		return providerPage{Provider: provider.key(), Title: provider.name(), Entries: []providerCollection{
			{Provider: provider.key(), Kind: "albums", Title: "Albums"},
			{Provider: provider.key(), Kind: "artists", Title: "Artists"},
			{Provider: provider.key(), Kind: "playlists", Title: "Playlists"},
		}}, nil
	}
	page := providerPage{Provider: provider.key(), Title: provider.name() + " " + kind}
	switch kind {
	case "albums":
		var root map[string]any
		err := provider.subsonicJSON(ctx, "getAlbumList2.view", url.Values{"type": {"alphabeticalByName"}, "size": {strconv.Itoa(request.Limit)}, "offset": {strconv.Itoa(request.Offset)}}, &root)
		for _, raw := range sliceValue(mapValue(root["albumList2"])["album"]) {
			album := mapValue(raw)
			page.Entries = append(page.Entries, providerCollection{Provider: provider.key(), Kind: "album", ID: stringValue(album["id"]), Title: stringValue(album["name"]), Subtitle: stringValue(album["artist"]), Count: int(numberValue(album["songCount"]))})
		}
		page.Next = providerNextOffset(request.Offset, request.Limit, len(page.Entries))
		return page, err
	case "album":
		var root map[string]any
		err := provider.subsonicJSON(ctx, "getAlbum.view", url.Values{"id": {request.ID}}, &root)
		album := mapValue(root["album"])
		page.Title = firstNonempty(stringValue(album["name"]), "Album")
		for _, raw := range sliceValue(album["song"]) {
			if item, ok := provider.subsonicItem(mapValue(raw)); ok {
				page.Items = append(page.Items, item)
			}
		}
		return page, err
	case "artists":
		var root map[string]any
		err := provider.subsonicJSON(ctx, "getArtists.view", nil, &root)
		for _, rawIndex := range sliceValue(mapValue(root["artists"])["index"]) {
			for _, rawArtist := range sliceValue(mapValue(rawIndex)["artist"]) {
				artist := mapValue(rawArtist)
				page.Entries = append(page.Entries, providerCollection{Provider: provider.key(), Kind: "artist", ID: stringValue(artist["id"]), Title: stringValue(artist["name"]), Count: int(numberValue(artist["albumCount"]))})
			}
		}
		return page, err
	case "artist":
		var root map[string]any
		err := provider.subsonicJSON(ctx, "getArtist.view", url.Values{"id": {request.ID}}, &root)
		artist := mapValue(root["artist"])
		page.Title = firstNonempty(stringValue(artist["name"]), "Artist")
		for _, raw := range sliceValue(artist["album"]) {
			album := mapValue(raw)
			page.Entries = append(page.Entries, providerCollection{Provider: provider.key(), Kind: "album", ID: stringValue(album["id"]), Title: stringValue(album["name"]), Subtitle: stringValue(album["artist"]), Count: int(numberValue(album["songCount"]))})
		}
		return page, err
	case "playlists":
		var root map[string]any
		err := provider.subsonicJSON(ctx, "getPlaylists.view", nil, &root)
		for _, raw := range sliceValue(mapValue(root["playlists"])["playlist"]) {
			playlist := mapValue(raw)
			page.Entries = append(page.Entries, providerCollection{Provider: provider.key(), Kind: "playlist", ID: stringValue(playlist["id"]), Title: stringValue(playlist["name"]), Count: int(numberValue(playlist["songCount"]))})
		}
		return page, err
	case "playlist":
		var root map[string]any
		err := provider.subsonicJSON(ctx, "getPlaylist.view", url.Values{"id": {request.ID}}, &root)
		playlist := mapValue(root["playlist"])
		page.Title = firstNonempty(stringValue(playlist["name"]), "Playlist")
		for _, raw := range sliceValue(playlist["entry"]) {
			if item, ok := provider.subsonicItem(mapValue(raw)); ok {
				page.Items = append(page.Items, item)
			}
		}
		return page, err
	default:
		return page, fmt.Errorf("unsupported browse kind %q", request.Kind)
	}
}

func (provider *httpMediaProvider) subsonicItem(song map[string]any) (MediaItem, bool) {
	id := stringValue(song["id"])
	if id == "" {
		return MediaItem{}, false
	}
	item := providerMediaItem(provider.key(), id, stringValue(song["title"]), stringValue(song["artist"]), stringValue(song["album"]), "", "", numberValue(song["duration"]))
	item.Genre = stringValue(song["genre"])
	item.ProviderMeta["cover_id"] = stringValue(song["coverArt"])
	return item, true
}

func (provider *httpMediaProvider) browseEmby(ctx context.Context, request providerBrowseRequest) (providerPage, error) {
	kind := strings.ToLower(request.Kind)
	if kind == "" || kind == "home" || kind == "music" {
		return providerPage{Provider: provider.key(), Title: provider.name(), Entries: []providerCollection{
			{Provider: provider.key(), Kind: "albums", Title: "Albums"},
			{Provider: provider.key(), Kind: "artists", Title: "Artists"},
			{Provider: provider.key(), Kind: "playlists", Title: "Playlists"},
		}}, nil
	}
	page := providerPage{Provider: provider.key(), Title: provider.name() + " " + kind}
	values := url.Values{"Recursive": {"true"}, "StartIndex": {strconv.Itoa(request.Offset)}, "Limit": {strconv.Itoa(request.Limit)}}
	if provider.config.UserID != "" {
		values.Set("UserId", provider.config.UserID)
	}
	switch kind {
	case "albums":
		values.Set("IncludeItemTypes", "MusicAlbum")
	case "artists":
		values.Set("IncludeItemTypes", "MusicArtist")
	case "playlists":
		values.Set("IncludeItemTypes", "Playlist")
	case "album":
		values.Set("IncludeItemTypes", "Audio")
		values.Set("ParentId", request.ID)
	case "artist":
		values.Set("IncludeItemTypes", "Audio")
		values.Set("ArtistIds", request.ID)
	case "playlist":
		values.Set("IncludeItemTypes", "Audio")
		values.Set("ParentId", request.ID)
	default:
		return page, fmt.Errorf("unsupported browse kind %q", request.Kind)
	}
	var response map[string]any
	if err := provider.getJSON(ctx, "/Items", values, &response); err != nil {
		return page, err
	}
	for _, raw := range sliceValue(response["Items"]) {
		entry := mapValue(raw)
		id := stringValue(entry["Id"])
		entryType := strings.ToLower(stringValue(entry["Type"]))
		if entryType == "audio" || kind == "album" || kind == "artist" || kind == "playlist" {
			page.Items = append(page.Items, provider.embyItem(entry))
			continue
		}
		childKind := map[string]string{"musicalbum": "album", "musicartist": "artist", "playlist": "playlist"}[entryType]
		page.Entries = append(page.Entries, providerCollection{Provider: provider.key(), Kind: childKind, ID: id, Title: stringValue(entry["Name"]), Subtitle: firstSliceString(entry["Artists"]), Count: int(numberValue(entry["ChildCount"]))})
	}
	page.Next = providerNextOffset(request.Offset, request.Limit, len(page.Items)+len(page.Entries))
	return page, nil
}

func (provider *httpMediaProvider) embyItem(entry map[string]any) MediaItem {
	id := stringValue(entry["Id"])
	item := providerMediaItem(provider.key(), id, stringValue(entry["Name"]), firstSliceString(entry["Artists"]), stringValue(entry["Album"]), "", "", numberValue(entry["RunTimeTicks"])/1e7)
	if tags := mapValue(entry["ImageTags"]); stringValue(tags["Primary"]) != "" {
		item.ProviderMeta["image_id"] = id
	}
	return item
}

func (provider *httpMediaProvider) browsePlex(ctx context.Context, request providerBrowseRequest) (providerPage, error) {
	kind := strings.ToLower(request.Kind)
	if kind == "" || kind == "home" || kind == "music" {
		return providerPage{Provider: provider.key(), Title: provider.name(), Entries: []providerCollection{
			{Provider: provider.key(), Kind: "albums", Title: "Albums"},
			{Provider: provider.key(), Kind: "artists", Title: "Artists"},
			{Provider: provider.key(), Kind: "playlists", Title: "Playlists"},
		}}, nil
	}
	page := providerPage{Provider: provider.key(), Title: provider.name() + " " + kind}
	if kind == "playlists" {
		var response map[string]any
		if err := provider.getJSON(ctx, "/playlists", nil, &response); err != nil {
			return page, err
		}
		for _, raw := range sliceValue(mapValue(response["MediaContainer"])["Metadata"]) {
			entry := mapValue(raw)
			if playlistType := stringValue(entry["playlistType"]); playlistType != "" && playlistType != "audio" {
				continue
			}
			page.Entries = append(page.Entries, providerCollection{Provider: provider.key(), Kind: "playlist", ID: stringValue(entry["ratingKey"]), Title: stringValue(entry["title"]), Count: int(numberValue(entry["leafCount"]))})
		}
		page.Entries, page.Next = pageProviderCollections(page.Entries, request.Offset, request.Limit)
		return page, nil
	}
	if kind == "album" || kind == "playlist" {
		path := "/library/metadata/" + url.PathEscape(request.ID) + "/children"
		if kind == "playlist" {
			path = "/playlists/" + url.PathEscape(request.ID) + "/items"
		}
		var response map[string]any
		values := url.Values{"X-Plex-Container-Start": {strconv.Itoa(request.Offset)}, "X-Plex-Container-Size": {strconv.Itoa(request.Limit)}}
		if err := provider.getJSON(ctx, path, values, &response); err != nil {
			return page, err
		}
		container := mapValue(response["MediaContainer"])
		for _, raw := range sliceValue(container["Metadata"]) {
			if item, ok := provider.plexJSONItem(mapValue(raw)); ok {
				page.Items = append(page.Items, item)
			}
		}
		page.Next = providerNextOffset(request.Offset, request.Limit, len(page.Items))
		return page, nil
	}
	if kind == "artist" {
		var response map[string]any
		if err := provider.getJSON(ctx, "/library/metadata/"+url.PathEscape(request.ID)+"/children", nil, &response); err != nil {
			return page, err
		}
		for _, raw := range sliceValue(mapValue(response["MediaContainer"])["Metadata"]) {
			entry := mapValue(raw)
			page.Entries = append(page.Entries, providerCollection{Provider: provider.key(), Kind: "album", ID: stringValue(entry["ratingKey"]), Title: stringValue(entry["title"]), Subtitle: stringValue(entry["parentTitle"]), Count: int(numberValue(entry["leafCount"]))})
		}
		page.Entries, page.Next = pageProviderCollections(page.Entries, request.Offset, request.Limit)
		return page, nil
	}
	if kind != "albums" && kind != "artists" {
		return page, fmt.Errorf("unsupported browse kind %q", request.Kind)
	}
	sections, err := provider.plexMusicSections(ctx)
	if err != nil {
		return page, err
	}
	typeID, childKind := "9", "album"
	if kind == "artists" {
		typeID, childKind = "8", "artist"
	}
	var entries []providerCollection
	totalEntries := 0
	moreOnServer := false
	for _, section := range sections {
		var response map[string]any
		requested := min(5000, request.Offset+request.Limit+1)
		values := url.Values{"type": {typeID}, "sort": {"titleSort:asc"}, "X-Plex-Container-Start": {"0"}, "X-Plex-Container-Size": {strconv.Itoa(requested)}}
		if err := provider.getJSON(ctx, "/library/sections/"+url.PathEscape(section)+"/all", values, &response); err != nil {
			return page, err
		}
		container := mapValue(response["MediaContainer"])
		metadata := sliceValue(container["Metadata"])
		total := int(numberValue(container["totalSize"]))
		if total == 0 {
			total = len(metadata)
		}
		totalEntries += total
		moreOnServer = moreOnServer || total > len(metadata)
		for _, raw := range metadata {
			entry := mapValue(raw)
			entries = append(entries, providerCollection{Provider: provider.key(), Kind: childKind, ID: stringValue(entry["ratingKey"]), Title: stringValue(entry["title"]), Subtitle: stringValue(entry["parentTitle"]), Count: int(numberValue(entry["leafCount"]))})
		}
	}
	slices.SortFunc(entries, func(a, b providerCollection) int {
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	})
	page.Entries, page.Next = pageProviderCollections(entries, request.Offset, request.Limit)
	if page.Next == 0 && request.Limit > 0 && request.Offset+len(page.Entries) < totalEntries && moreOnServer {
		page.Next = request.Offset + len(page.Entries)
	}
	return page, nil
}

func (provider *httpMediaProvider) plexMusicSections(ctx context.Context) ([]string, error) {
	var response map[string]any
	if err := provider.getJSON(ctx, "/library/sections", nil, &response); err != nil {
		return nil, err
	}
	var sections []string
	for _, raw := range sliceValue(mapValue(response["MediaContainer"])["Directory"]) {
		entry := mapValue(raw)
		if stringValue(entry["type"]) != "artist" || !provider.includesLibrary(stringValue(entry["title"])) {
			continue
		}
		if key := stringValue(entry["key"]); key != "" {
			sections = append(sections, key)
		}
	}
	return sections, nil
}

func (provider *httpMediaProvider) includesLibrary(title string) bool {
	return len(provider.config.Libraries) == 0 || slices.ContainsFunc(provider.config.Libraries, func(value string) bool { return strings.EqualFold(strings.TrimSpace(value), title) })
}

func (provider *httpMediaProvider) plexJSONItem(entry map[string]any) (MediaItem, bool) {
	id := stringValue(entry["ratingKey"])
	media := sliceValue(entry["Media"])
	if id == "" || len(media) == 0 {
		return MediaItem{}, false
	}
	parts := sliceValue(mapValue(media[0])["Part"])
	if len(parts) == 0 || stringValue(mapValue(parts[0])["key"]) == "" {
		return MediaItem{}, false
	}
	item := providerMediaItem(provider.key(), id, stringValue(entry["title"]), stringValue(entry["grandparentTitle"]), stringValue(entry["parentTitle"]), "", "", numberValue(entry["duration"])/1000)
	item.ProviderMeta["part"] = stringValue(mapValue(parts[0])["key"])
	item.ProviderMeta["thumb"] = stringValue(entry["thumb"])
	return item, true
}

func pageProviderCollections(entries []providerCollection, offset, limit int) ([]providerCollection, int) {
	if offset >= len(entries) {
		return []providerCollection{}, 0
	}
	end := min(len(entries), offset+limit)
	next := 0
	if end < len(entries) {
		next = end
	}
	return entries[offset:end], next
}

func (provider *httpMediaProvider) browseSpotify(ctx context.Context, request providerBrowseRequest) (providerPage, error) {
	kind := strings.ToLower(request.Kind)
	if kind == "" || kind == "home" || kind == "music" {
		return providerPage{Provider: provider.key(), Title: provider.name(), Entries: []providerCollection{
			{Provider: provider.key(), Kind: "playlists", Title: "Playlists"},
			{Provider: provider.key(), Kind: "albums", Title: "Saved Albums"},
			{Provider: provider.key(), Kind: "favorites", Title: "Liked Tracks"},
		}}, nil
	}
	page := providerPage{Provider: provider.key(), Title: "Spotify " + kind}
	values := url.Values{"limit": {strconv.Itoa(min(request.Limit, 50))}, "offset": {strconv.Itoa(request.Offset)}}
	path := ""
	switch kind {
	case "playlists":
		path = "/v1/me/playlists"
	case "albums":
		path = "/v1/me/albums"
	case "favorites":
		path = "/v1/me/tracks"
	case "playlist":
		path = "/v1/playlists/" + url.PathEscape(request.ID) + "/tracks"
	case "album":
		path = "/v1/albums/" + url.PathEscape(request.ID) + "/tracks"
	default:
		return page, fmt.Errorf("unsupported browse kind %q", request.Kind)
	}
	var response map[string]any
	if err := provider.getJSON(ctx, path, values, &response); err != nil {
		return page, err
	}
	for _, raw := range sliceValue(response["items"]) {
		entry := mapValue(raw)
		if wrapped := mapValue(entry["track"]); len(wrapped) > 0 {
			entry = wrapped
		} else if wrapped := mapValue(entry["album"]); len(wrapped) > 0 && kind == "albums" {
			entry = wrapped
		}
		id := stringValue(entry["id"])
		if kind == "playlists" || kind == "albums" {
			page.Entries = append(page.Entries, providerCollection{Provider: provider.key(), Kind: strings.TrimSuffix(kind, "s"), ID: id, Title: stringValue(entry["name"]), Subtitle: firstSliceMapString(entry["artists"], "name"), Artwork: firstImage(entry["images"]), Count: int(numberValue(mapValue(entry["tracks"])["total"]))})
			continue
		}
		page.Items = append(page.Items, provider.spotifyItem(entry))
	}
	page.Next = providerNextOffset(request.Offset, min(request.Limit, 50), len(page.Items)+len(page.Entries))
	return page, nil
}

func (provider *httpMediaProvider) spotifyItem(track map[string]any) MediaItem {
	album := mapValue(track["album"])
	item := providerMediaItem(provider.key(), stringValue(track["id"]), stringValue(track["name"]), firstSliceMapString(track["artists"], "name"), stringValue(album["name"]), firstImage(album["images"]), stringValue(mapValue(track["external_urls"])["spotify"]), numberValue(track["duration_ms"])/1000)
	provider.setPreview(&item, stringValue(track["preview_url"]))
	return item
}

func (provider *httpMediaProvider) browseAudiobookshelf(ctx context.Context, request providerBrowseRequest) (providerPage, error) {
	kind := strings.ToLower(request.Kind)
	page := providerPage{Provider: provider.key(), Title: "Audiobookshelf"}
	if kind == "" || kind == "home" || kind == "music" || kind == "libraries" {
		var response map[string]any
		if err := provider.getJSON(ctx, "/api/libraries", nil, &response); err != nil {
			return page, err
		}
		for _, raw := range sliceValue(response["libraries"]) {
			library := mapValue(raw)
			page.Entries = append(page.Entries, providerCollection{Provider: provider.key(), Kind: "library", ID: stringValue(library["id"]), Title: stringValue(library["name"])})
		}
		return page, nil
	}
	if kind == "item" {
		items, err := provider.audiobookshelfTracks(ctx, map[string]any{"id": request.ID})
		page.Items = items
		return page, err
	}
	if kind != "library" {
		items, err := provider.search(ctx, firstNonempty(request.ID, kind), request.Limit)
		page.Items = items
		return page, err
	}
	var response map[string]any
	path := "/api/libraries/" + url.PathEscape(request.ID) + "/items"
	if err := provider.getJSON(ctx, path, url.Values{"limit": {strconv.Itoa(request.Limit)}, "page": {strconv.Itoa(request.Offset / max(1, request.Limit))}}, &response); err != nil {
		return page, err
	}
	for _, raw := range sliceValue(response["results"]) {
		entry := mapValue(raw)
		media := mapValue(entry["media"])
		metadata := mapValue(media["metadata"])
		id := stringValue(entry["id"])
		page.Entries = append(page.Entries, providerCollection{Provider: provider.key(), Kind: "item", ID: id, Title: firstNonempty(stringValue(metadata["title"]), stringValue(entry["title"])), Subtitle: firstNonempty(stringValue(metadata["authorName"]), stringValue(metadata["author"])), Count: len(sliceValue(media["tracks"]))})
	}
	page.Next = providerNextOffset(request.Offset, request.Limit, len(page.Entries))
	return page, nil
}

func providerNextOffset(offset, limit, count int) int {
	if limit > 0 && count >= limit {
		return offset + count
	}
	return 0
}

func (provider *httpMediaProvider) resolve(ctx context.Context, item MediaItem) (providerStream, error) {
	switch provider.kind {
	case "navidrome", "subsonic":
		streamURL, err := provider.subsonicURL("stream.view", url.Values{"id": {item.ProviderID}})
		return providerStream{URL: streamURL}, err
	case "jellyfin", "emby":
		base, err := provider.baseURL()
		if err != nil {
			return providerStream{}, err
		}
		u := appendProviderPath(base, "/Audio/"+url.PathEscape(item.ProviderID)+"/stream")
		return providerStream{URL: u.String(), Headers: map[string]string{"X-Emby-Token": provider.secret.Token}}, nil
	case "plex":
		part := item.ProviderMeta["part"]
		if part == "" {
			return providerStream{}, errors.New("plex result has no audio part; search again")
		}
		base, err := provider.baseURL()
		if err != nil {
			return providerStream{}, err
		}
		u := appendProviderPath(base, part)
		return providerStream{URL: u.String(), Headers: map[string]string{"X-Plex-Token": provider.secret.Token}}, nil
	case "audiobookshelf":
		ino := item.ProviderMeta["ino"]
		itemID := item.ProviderMeta["item_id"]
		if ino == "" || itemID == "" {
			return providerStream{}, errors.New("audiobookshelf result has no audio file; search again")
		}
		base, err := provider.baseURL()
		if err != nil {
			return providerStream{}, err
		}
		u := appendProviderPath(base, "/api/items/"+url.PathEscape(itemID)+"/file/"+url.PathEscape(ino))
		return providerStream{URL: u.String(), Headers: map[string]string{"Authorization": "Bearer " + provider.secret.Token}}, nil
	case "spotify", "tidal", "qobuz":
		if item.ProviderMeta["playback"] == "preview" {
			streamURL := item.ProviderMeta["preview_url"]
			if streamURL == "" {
				return providerStream{}, fmt.Errorf("%s did not return a preview for this track", provider.name())
			}
			return providerStream{URL: streamURL}, nil
		}
		return providerStream{}, fmt.Errorf("%s exposes catalog metadata but no full-track stream; this track has no preview", provider.name())
	default:
		return providerStream{}, errors.New("provider cannot resolve playback")
	}
}

func (provider *httpMediaProvider) loadArtwork(ctx context.Context, item MediaItem) (string, error) {
	for _, extension := range []string{".jpg", ".png", ".webp", ".gif", ".svg"} {
		path := providerArtworkPath(item, extension)
		if path != "" {
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}
	var artworkURL string
	switch provider.kind {
	case "navidrome", "subsonic":
		coverID := item.ProviderMeta["cover_id"]
		if coverID == "" {
			return "", nil
		}
		var err error
		artworkURL, err = provider.subsonicURL("getCoverArt.view", url.Values{"id": {coverID}})
		if err != nil {
			return "", err
		}
	case "jellyfin", "emby":
		imageID := item.ProviderMeta["image_id"]
		if imageID == "" {
			return "", nil
		}
		base, err := provider.baseURL()
		if err != nil {
			return "", err
		}
		artworkURL = appendProviderPath(base, "/Items/"+url.PathEscape(imageID)+"/Images/Primary").String()
	case "plex":
		thumb := item.ProviderMeta["thumb"]
		if thumb == "" {
			return "", nil
		}
		base, err := provider.baseURL()
		if err != nil {
			return "", err
		}
		artworkURL = appendProviderPath(base, thumb).String()
	case "audiobookshelf":
		itemID := item.ProviderMeta["item_id"]
		if itemID == "" {
			return "", nil
		}
		base, err := provider.baseURL()
		if err != nil {
			return "", err
		}
		artworkURL = appendProviderPath(base, "/api/items/"+url.PathEscape(itemID)+"/cover").String()
	default:
		return "", nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artworkURL, nil)
	if err != nil {
		return "", err
	}
	provider.authorize(request)
	response, err := provider.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("artwork server returned %s", response.Status)
	}
	const artworkLimit = 12 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, artworkLimit+1))
	if err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > artworkLimit {
		return "", errors.New("provider artwork is empty or too large")
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
	extension := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif", "image/svg+xml": ".svg"}[contentType]
	if extension == "" {
		return "", fmt.Errorf("provider artwork has unsupported type %q", contentType)
	}
	path := providerArtworkPath(item, extension)
	if path == "" {
		return "", errors.New("provider artwork cache is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".provider-artwork-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		var written int
		written, err = writeProviderArtwork(file, data)
		if err == nil && written != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func providerArtworkPath(item MediaItem, extension string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "chill", "provider-artwork", item.ID+extension)
}

func (provider *httpMediaProvider) baseURL() (*url.URL, error) {
	raw := strings.TrimRight(provider.config.URL, "/")
	if raw == "" {
		return nil, errors.New("provider URL is not configured")
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("provider URL must be absolute HTTP(S) without credentials")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return nil, errors.New("remote provider URLs must use HTTPS")
	}
	return u, nil
}

func appendProviderPath(base *url.URL, endpoint string) *url.URL {
	result := *base
	result.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(endpoint, "/")
	result.RawPath, result.RawQuery, result.Fragment = "", "", ""
	return &result
}

func (provider *httpMediaProvider) authorize(request *http.Request) {
	switch provider.kind {
	case "jellyfin", "emby":
		request.Header.Set("X-Emby-Token", provider.secret.Token)
	case "plex":
		request.Header.Set("X-Plex-Token", provider.secret.Token)
	case "audiobookshelf", "spotify", "tidal":
		request.Header.Set("Authorization", "Bearer "+provider.secret.Token)
	case "qobuz":
		request.Header.Set("X-App-Id", provider.config.ClientID)
		request.Header.Set("X-User-Auth-Token", provider.secret.Token)
	}
}

func (provider *httpMediaProvider) getJSON(ctx context.Context, path string, query url.Values, output any) error {
	return provider.requestJSON(ctx, http.MethodGet, path, query, nil, output)
}

func (provider *httpMediaProvider) requestJSON(ctx context.Context, method, path string, query url.Values, input, output any) error {
	base, err := provider.baseURL()
	if err != nil {
		return err
	}
	u := appendProviderPath(base, path)
	if query != nil {
		u.RawQuery = query.Encode()
	}
	var body io.Reader
	if input != nil {
		data, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			return marshalErr
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	provider.authorize(request)
	response, err := provider.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.UnmarshalRead(io.LimitReader(response.Body, providerResponseLimit), output)
}

func (provider *httpMediaProvider) subsonicURL(method string, values url.Values) (string, error) {
	base, err := provider.baseURL()
	if err != nil {
		return "", err
	}
	if values == nil {
		values = url.Values{}
	}
	values.Set("u", provider.config.Username)
	values.Set("v", "1.16.1")
	values.Set("c", "chill")
	values.Set("f", "json")
	if provider.secret.Token != "" && provider.secret.Password == "" {
		values.Set("p", "enc:"+hex.EncodeToString([]byte(provider.secret.Token)))
	} else {
		salt := strconv.FormatUint(rand.Uint64(), 36)
		digest := md5.Sum([]byte(provider.secret.Password + salt))
		values.Set("s", salt)
		values.Set("t", hex.EncodeToString(digest[:]))
	}
	u := appendProviderPath(base, "/rest/"+method)
	u.RawQuery = values.Encode()
	return u.String(), nil
}

func (provider *httpMediaProvider) subsonicJSON(ctx context.Context, method string, values url.Values, output any) error {
	endpoint, err := provider.subsonicURL(method, values)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := provider.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s", response.Status)
	}
	var envelope map[string]any
	if err := json.UnmarshalRead(io.LimitReader(response.Body, providerResponseLimit), &envelope); err != nil {
		return err
	}
	root := mapValue(envelope["subsonic-response"])
	if status := stringValue(root["status"]); status != "ok" {
		failure := mapValue(root["error"])
		return fmt.Errorf("subsonic error: %s", stringValue(failure["message"]))
	}
	data, err := json.Marshal(root)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, output)
}

func (provider *httpMediaProvider) searchSubsonic(ctx context.Context, query string, limit int) ([]MediaItem, error) {
	var root map[string]any
	err := provider.subsonicJSON(ctx, "search3.view", url.Values{"query": {query}, "songCount": {strconv.Itoa(limit)}, "albumCount": {"0"}, "artistCount": {"0"}}, &root)
	if err != nil {
		return nil, err
	}
	result := mapValue(root["searchResult3"])
	var items []MediaItem
	for _, raw := range sliceValue(result["song"]) {
		song := mapValue(raw)
		id := stringValue(song["id"])
		if id == "" {
			continue
		}
		item := providerMediaItem(provider.key(), id, stringValue(song["title"]), stringValue(song["artist"]), stringValue(song["album"]), "", "", numberValue(song["duration"]))
		item.Genre = stringValue(song["genre"])
		item.ProviderMeta["cover_id"] = stringValue(song["coverArt"])
		items = append(items, item)
	}
	return items, nil
}

func (provider *httpMediaProvider) searchEmby(ctx context.Context, query string, limit int) ([]MediaItem, error) {
	values := url.Values{"Recursive": {"true"}, "SearchTerm": {query}, "IncludeItemTypes": {"Audio"}, "Fields": {"Genres,MediaSources,Path"}, "Limit": {strconv.Itoa(limit)}}
	if provider.config.UserID != "" {
		values.Set("UserId", provider.config.UserID)
	}
	var response map[string]any
	if err := provider.getJSON(ctx, "/Items", values, &response); err != nil {
		return nil, err
	}
	var items []MediaItem
	for _, raw := range sliceValue(response["Items"]) {
		entry := mapValue(raw)
		id := stringValue(entry["Id"])
		if id == "" {
			continue
		}
		items = append(items, provider.embyItem(entry))
	}
	return items, nil
}

type plexContainer struct {
	Tracks []plexTrack `xml:"Track"` // Tracks contains matching audio leaves.
}

type plexTrack struct {
	RatingKey string      `xml:"ratingKey,attr"`        // RatingKey is the stable Plex metadata id.
	Title     string      `xml:"title,attr"`            // Title is the track title.
	Artist    string      `xml:"grandparentTitle,attr"` // Artist is the album artist.
	Album     string      `xml:"parentTitle,attr"`      // Album is the parent album.
	Duration  float64     `xml:"duration,attr"`         // Duration is milliseconds.
	Thumb     string      `xml:"thumb,attr"`            // Thumb is an artwork path.
	Media     []plexMedia `xml:"Media"`                 // Media contains encoded versions.
}

type plexMedia struct {
	Parts []plexPart `xml:"Part"` // Parts contains playable files.
}

type plexPart struct {
	Key string `xml:"key,attr"` // Key is the authenticated stream path.
}

func (provider *httpMediaProvider) searchPlex(ctx context.Context, query string, limit int) ([]MediaItem, error) {
	tracks, err := provider.plexTracks(ctx, "/search", url.Values{"query": {query}})
	if err != nil {
		return nil, err
	}
	var items []MediaItem
	for _, track := range tracks {
		if len(items) >= limit || track.RatingKey == "" || len(track.Media) == 0 || len(track.Media[0].Parts) == 0 {
			continue
		}
		items = append(items, provider.plexItem(track))
	}
	return items, nil
}

func (provider *httpMediaProvider) plexTracks(ctx context.Context, endpoint string, values url.Values) ([]plexTrack, error) {
	base, err := provider.baseURL()
	if err != nil {
		return nil, err
	}
	u := appendProviderPath(base, endpoint)
	if values == nil {
		values = url.Values{}
	}
	u.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	provider.authorize(request)
	response, err := provider.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plex returned %s", response.Status)
	}
	var container plexContainer
	if err := xml.NewDecoder(io.LimitReader(response.Body, providerResponseLimit)).Decode(&container); err != nil {
		return nil, err
	}
	return container.Tracks, nil
}

func (provider *httpMediaProvider) plexItem(track plexTrack) MediaItem {
	item := providerMediaItem(provider.key(), track.RatingKey, track.Title, track.Artist, track.Album, "", "", track.Duration/1000)
	if len(track.Media) > 0 && len(track.Media[0].Parts) > 0 {
		item.ProviderMeta["part"] = track.Media[0].Parts[0].Key
	}
	item.ProviderMeta["thumb"] = track.Thumb
	return item
}

func (provider *httpMediaProvider) searchAudiobookshelf(ctx context.Context, query string, limit int) ([]MediaItem, error) {
	var libraries map[string]any
	if err := provider.getJSON(ctx, "/api/libraries", nil, &libraries); err != nil {
		return nil, err
	}
	var items []MediaItem
	for _, rawLibrary := range sliceValue(libraries["libraries"]) {
		library := mapValue(rawLibrary)
		libraryID := stringValue(library["id"])
		if libraryID == "" {
			continue
		}
		var response map[string]any
		if err := provider.getJSON(ctx, "/api/libraries/"+url.PathEscape(libraryID)+"/search", url.Values{"q": {query}, "limit": {strconv.Itoa(limit)}}, &response); err != nil {
			return nil, err
		}
		for _, group := range []string{"book", "podcast"} {
			for _, rawHit := range sliceValue(response[group]) {
				hit := mapValue(rawHit)
				libraryItem := mapValue(hit["libraryItem"])
				expanded, err := provider.audiobookshelfTracks(ctx, libraryItem)
				if err == nil {
					items = append(items, expanded...)
				}
				if len(items) >= limit {
					return items[:limit], nil
				}
			}
		}
	}
	return items, nil
}

func (provider *httpMediaProvider) audiobookshelfTracks(ctx context.Context, summary map[string]any) ([]MediaItem, error) {
	itemID := stringValue(summary["id"])
	if itemID == "" {
		return nil, errors.New("audiobookshelf result has no id")
	}
	var item map[string]any
	if err := provider.getJSON(ctx, "/api/items/"+url.PathEscape(itemID), url.Values{"expanded": {"1"}}, &item); err != nil {
		return nil, err
	}
	media := mapValue(item["media"])
	metadata := mapValue(media["metadata"])
	title, artist := stringValue(metadata["title"]), firstNonempty(stringValue(metadata["authorName"]), stringValue(metadata["author"]))
	tracks := sliceValue(media["tracks"])
	totalDuration := numberValue(media["duration"])
	for _, rawTrack := range tracks {
		track := mapValue(rawTrack)
		totalDuration = max(totalDuration, numberValue(track["startOffset"])+numberValue(track["duration"]))
	}
	var results []MediaItem
	for _, rawTrack := range tracks {
		track := mapValue(rawTrack)
		ino := stringValue(track["ino"])
		if ino == "" {
			continue
		}
		id := itemID + ":" + ino
		item := providerMediaItem(provider.key(), id, firstNonempty(stringValue(track["title"]), title), artist, title, "", "", numberValue(track["duration"]))
		item.ProviderMeta["item_id"], item.ProviderMeta["ino"] = itemID, ino
		item.ProviderMeta["progress_offset"] = strconv.FormatFloat(numberValue(track["startOffset"]), 'f', -1, 64)
		item.ProviderMeta["progress_duration"] = strconv.FormatFloat(totalDuration, 'f', -1, 64)
		if numberValue(track["startOffset"])+numberValue(track["duration"]) >= totalDuration-1 {
			item.ProviderMeta["progress_final"] = "true"
		}
		results = append(results, item)
	}
	for _, rawEpisode := range sliceValue(media["episodes"]) {
		episode := mapValue(rawEpisode)
		audioFile := mapValue(episode["audioFile"])
		ino, episodeID := stringValue(audioFile["ino"]), stringValue(episode["id"])
		if ino == "" || episodeID == "" {
			continue
		}
		item := providerMediaItem(provider.key(), itemID+":"+episodeID, stringValue(episode["title"]), artist, title, "", "", numberValue(audioFile["duration"]))
		item.ProviderMeta["item_id"], item.ProviderMeta["ino"], item.ProviderMeta["episode_id"] = itemID, ino, episodeID
		results = append(results, item)
	}
	return results, nil
}

func (provider *httpMediaProvider) searchSpotify(ctx context.Context, query string, limit int) ([]MediaItem, error) {
	if provider.secret.Token == "" {
		return nil, errors.New("spotify access token is required")
	}
	var response map[string]any
	if err := provider.getJSON(ctx, "/v1/search", url.Values{"q": {query}, "type": {"track"}, "limit": {strconv.Itoa(min(limit, 50))}}, &response); err != nil {
		return nil, err
	}
	tracks := mapValue(response["tracks"])
	var items []MediaItem
	for _, raw := range sliceValue(tracks["items"]) {
		track := mapValue(raw)
		id := stringValue(track["id"])
		album := mapValue(track["album"])
		artists := sliceValue(track["artists"])
		artist := ""
		if len(artists) > 0 {
			artist = stringValue(mapValue(artists[0])["name"])
		}
		artwork := firstImage(album["images"])
		item := providerMediaItem(provider.key(), id, stringValue(track["name"]), artist, stringValue(album["name"]), artwork, stringValue(mapValue(track["external_urls"])["spotify"]), numberValue(track["duration_ms"])/1000)
		provider.setPreview(&item, stringValue(track["preview_url"]))
		items = append(items, item)
	}
	return items, nil
}

func (provider *httpMediaProvider) searchOpenCatalog(ctx context.Context, query string, limit int) ([]MediaItem, error) {
	if provider.config.URL == "" {
		return nil, fmt.Errorf("%s API URL is required", provider.name())
	}
	path := "/search"
	values := url.Values{"query": {query}, "limit": {strconv.Itoa(limit)}}
	if provider.kind == "qobuz" {
		path, values = "/catalog/search", url.Values{"query": {query}, "limit": {strconv.Itoa(limit)}, "app_id": {provider.config.ClientID}}
	}
	var response map[string]any
	if err := provider.getJSON(ctx, path, values, &response); err != nil {
		return nil, err
	}
	tracks := sliceValue(mapValue(response["tracks"])["items"])
	if len(tracks) == 0 {
		tracks = sliceValue(response["items"])
	}
	var items []MediaItem
	for _, raw := range tracks {
		if item, ok := provider.openCatalogItem(mapValue(raw)); ok {
			items = append(items, item)
		}
	}
	return items, nil
}

func (provider *httpMediaProvider) openCatalogItem(track map[string]any) (MediaItem, bool) {
	id := identifierValue(track["id"])
	if id == "" {
		return MediaItem{}, false
	}
	artist := firstNonempty(stringValue(mapValue(track["performer"])["name"]), stringValue(mapValue(track["artist"])["name"]))
	album := mapValue(track["album"])
	artwork := firstNonempty(stringValue(mapValue(album["image"])["large"]), stringValue(track["imageCover"]))
	item := providerMediaItem(provider.key(), id, firstNonempty(stringValue(track["title"]), stringValue(track["name"])), artist, stringValue(album["title"]), artwork, stringValue(track["url"]), numberValue(track["duration"]))
	provider.setPreview(&item, firstNonempty(stringValue(track["preview_url"]), stringValue(track["previewUrl"])))
	return item, true
}

func (provider *httpMediaProvider) setPreview(item *MediaItem, previewURL string) {
	item.ProviderMeta["playback"] = "catalog"
	if previewURL == "" {
		return
	}
	item.ProviderMeta["catalog_duration"] = strconv.FormatFloat(item.Duration, 'f', -1, 64)
	item.ProviderMeta["playback"] = "preview"
	item.ProviderMeta["preview_url"] = previewURL
	item.Duration = min(item.Duration, 30)
	if item.Duration <= 0 {
		item.Duration = 30
	}
}

func (provider *httpMediaProvider) reportProgress(ctx context.Context, item MediaItem, position, duration time.Duration, state string) error {
	return provider.reportPlaybackProgress(ctx, item, providerProgressUpdate{item: item, position: position, duration: duration, state: state, completed: state == "finished", scrobble: state == "finished"})
}

func (provider *httpMediaProvider) reportPlaybackProgress(ctx context.Context, item MediaItem, progress providerProgressUpdate) error {
	position, duration, state := progress.position, progress.duration, progress.state
	switch provider.kind {
	case "navidrome", "subsonic":
		if state != "started" && !progress.scrobble {
			return nil
		}
		var output map[string]any
		return provider.subsonicJSON(ctx, "scrobble.view", url.Values{"id": {item.ProviderID}, "submission": {strconv.FormatBool(progress.scrobble)}, "time": {strconv.FormatInt(time.Now().UnixMilli(), 10)}}, &output)
	case "jellyfin", "emby":
		endpoint := "/Sessions/Playing/Progress"
		if state == "started" {
			endpoint = "/Sessions/Playing"
		} else if state == "finished" || state == "stopped" {
			endpoint = "/Sessions/Playing/Stopped"
		}
		payload := map[string]any{"ItemId": item.ProviderID, "PositionTicks": position.Nanoseconds() / 100, "IsPaused": state == "paused"}
		return provider.requestJSON(ctx, http.MethodPost, endpoint, nil, payload, nil)
	case "plex":
		values := url.Values{"ratingKey": {item.ProviderID}, "key": {"/library/metadata/" + item.ProviderID}, "time": {strconv.FormatInt(position.Milliseconds(), 10)}, "duration": {strconv.FormatInt(duration.Milliseconds(), 10)}, "state": {map[string]string{"finished": "stopped", "stopped": "stopped", "paused": "paused"}[state]}}
		if values.Get("state") == "" {
			values.Set("state", "playing")
		}
		return provider.requestJSON(ctx, http.MethodGet, "/:/timeline", values, nil, nil)
	case "audiobookshelf":
		itemID := firstNonempty(item.ProviderMeta["item_id"], item.ProviderID)
		path := "/api/me/progress/" + url.PathEscape(itemID)
		finished := false
		if episodeID := item.ProviderMeta["episode_id"]; episodeID != "" {
			path += "/" + url.PathEscape(episodeID)
			finished = progress.completed || state == "stopped" && duration > 0 && position >= duration-time.Second
		} else {
			offset, _ := strconv.ParseFloat(item.ProviderMeta["progress_offset"], 64)
			total, _ := strconv.ParseFloat(item.ProviderMeta["progress_duration"], 64)
			position += time.Duration(offset * float64(time.Second))
			if total > 0 {
				duration = time.Duration(total * float64(time.Second))
			}
			atEnd := duration > 0 && position >= duration-time.Second
			finished = progress.completed && item.ProviderMeta["progress_final"] == "true" || (state == "finished" || state == "stopped") && atEnd
		}
		payload := map[string]any{"currentTime": position.Seconds(), "duration": duration.Seconds(), "isFinished": finished}
		return provider.requestJSON(ctx, http.MethodPatch, path, nil, payload, nil)
	default:
		return nil
	}
}

func (provider *httpMediaProvider) setFavorite(ctx context.Context, item MediaItem, favorite bool) error {
	switch provider.kind {
	case "navidrome", "subsonic":
		method := "star.view"
		if !favorite {
			method = "unstar.view"
		}
		var output map[string]any
		return provider.subsonicJSON(ctx, method, url.Values{"id": {item.ProviderID}}, &output)
	case "jellyfin", "emby":
		if provider.config.UserID == "" {
			return errors.New("provider user id is required to synchronize favorites")
		}
		method := http.MethodPost
		if !favorite {
			method = http.MethodDelete
		}
		path := "/Users/" + url.PathEscape(provider.config.UserID) + "/FavoriteItems/" + url.PathEscape(item.ProviderID)
		return provider.requestJSON(ctx, method, path, nil, nil, nil)
	case "spotify":
		method := http.MethodPut
		if !favorite {
			method = http.MethodDelete
		}
		return provider.requestJSON(ctx, method, "/v1/me/tracks", url.Values{"ids": {item.ProviderID}}, nil, nil)
	case "tidal", "qobuz":
		method := http.MethodPut
		if !favorite {
			method = http.MethodDelete
		}
		return provider.requestJSON(ctx, method, "/favorites/tracks/"+url.PathEscape(item.ProviderID), nil, nil, nil)
	default:
		return nil
	}
}

func mapValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func sliceValue(value any) []any {
	result, _ := value.([]any)
	return result
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func identifierValue(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case float64:
		if value < 0 || value != float64(int64(value)) {
			return ""
		}
		return strconv.FormatFloat(value, 'f', 0, 64)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case uint64:
		return strconv.FormatUint(value, 10)
	default:
		return ""
	}
}

func numberValue(value any) float64 {
	switch value := value.(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	case string:
		result, _ := strconv.ParseFloat(value, 64)
		return result
	default:
		return 0
	}
}

func firstSliceString(value any) string {
	values := sliceValue(value)
	if len(values) == 0 {
		return ""
	}
	if direct, ok := values[0].(string); ok {
		return direct
	}
	return stringValue(mapValue(values[0])["Name"])
}

func firstSliceMapString(value any, key string) string {
	values := sliceValue(value)
	if len(values) == 0 {
		return ""
	}
	return stringValue(mapValue(values[0])[key])
}

func firstImage(value any) string {
	values := sliceValue(value)
	if len(values) == 0 {
		return ""
	}
	return stringValue(mapValue(values[0])["url"])
}
