package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

type playlistMutation struct {
	Name    string      `json:"name"`               // Name selects the saved playlist.
	NewName string      `json:"new_name,omitempty"` // NewName is used for rename.
	Items   []MediaItem `json:"items,omitempty"`    // Items replaces the saved contents.
}

func (d *Daemon) playlistCommand(action, arg string) string {
	library, err := d.mediaLibrary()
	if err != nil {
		return fail(err.Error())
	}
	if action == "library-state" {
		data, _ := json.Marshal(library)
		return ok(string(data))
	}
	var request playlistMutation
	if err := json.Unmarshal([]byte(arg), &request); err != nil {
		return fail("invalid playlist request")
	}
	key := playlistKey(request.Name)
	if key == "" || len([]rune(strings.TrimSpace(request.Name))) > 200 {
		return fail("playlist name must be between 1 and 200 characters")
	}
	message := "playlist saved"
	switch action {
	case "playlist-put":
		if len(request.Items) > 5000 {
			return fail("playlist is limited to 5000 items")
		}
		if _, exists := library.Playlists[key]; !exists && len(library.Playlists) >= 5000 {
			return fail("playlist limit reached")
		}
		now := time.Now().UTC()
		created := now
		if existing, ok := library.Playlists[key]; ok {
			created = existing.CreatedAt
		}
		for i := range request.Items {
			request.Items[i], err = request.Items[i].normalized()
			if err != nil {
				return fail(err.Error())
			}
		}
		library.Playlists[key] = savedPlaylist{Name: strings.TrimSpace(request.Name), Items: request.Items, CreatedAt: created, UpdatedAt: now}
	case "playlist-delete":
		if _, ok := library.Playlists[key]; !ok {
			return fail("playlist not found: " + request.Name)
		}
		delete(library.Playlists, key)
		message = "playlist deleted"
	case "playlist-rename":
		playlist, ok := library.Playlists[key]
		if !ok {
			return fail("playlist not found: " + request.Name)
		}
		newKey := playlistKey(request.NewName)
		if newKey == "" || len([]rune(strings.TrimSpace(request.NewName))) > 200 {
			return fail("new playlist name must be between 1 and 200 characters")
		}
		if _, exists := library.Playlists[newKey]; exists && newKey != key {
			return fail("playlist already exists: " + request.NewName)
		}
		delete(library.Playlists, key)
		playlist.Name, playlist.UpdatedAt = strings.TrimSpace(request.NewName), time.Now().UTC()
		library.Playlists[newKey] = playlist
		message = "playlist renamed"
	default:
		return fail("unknown playlist command")
	}
	if err := library.commit(); err != nil {
		return fail(err.Error())
	}
	return ok(message)
}

func fetchLibraryState() (*libraryState, error) {
	if !isDaemonRunning() {
		return loadLibrary()
	}
	out, err := ask("library-state")
	if err != nil {
		return nil, err
	}
	var library libraryState
	if err := json.Unmarshal([]byte(out), &library); err != nil {
		return nil, err
	}
	return &library, nil
}

func sendItems(action string, items []MediaItem) (string, error) {
	if err := ensureDaemon(); err != nil {
		return "", err
	}
	data, err := json.Marshal(queueRequest{Items: items})
	if err != nil {
		return "", err
	}
	return ask(action + " " + string(data))
}

func playMedia(item MediaItem, restart bool) (string, error) {
	if err := checkMediaRequirements([]MediaItem{item}); err != nil {
		return "", err
	}
	if err := ensureDaemon(); err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Item    MediaItem `json:"item"`
		Restart bool      `json:"restart,omitempty"`
	}{item, restart})
	if err != nil {
		return "", err
	}
	return ask("media-play " + string(data))
}

func playMediaItems(items []MediaItem) (string, error) {
	if len(items) == 0 {
		return "", fmt.Errorf("no playable media found")
	}
	if len(items) > 5000 {
		return "", fmt.Errorf("media session is limited to 5000 items")
	}
	if err := checkMediaRequirements(items); err != nil {
		return "", err
	}
	if err := ensureDaemon(); err != nil {
		return "", err
	}
	data, err := json.Marshal(queueRequest{Items: items})
	if err != nil {
		return "", err
	}
	return ask("media-session " + string(data))
}

func queueState() ([]MediaItem, []MediaItem, bool, string, error) {
	if !isDaemonRunning() {
		library, err := loadLibrary()
		if err != nil {
			return nil, nil, false, "", err
		}
		return library.Queue, library.PlayNext, library.Shuffle, library.Repeat, nil
	}
	out, err := ask("queue-list")
	if err != nil {
		return nil, nil, false, "", err
	}
	var state struct {
		Queue    []MediaItem `json:"queue"`
		PlayNext []MediaItem `json:"play_next"`
		Shuffle  bool        `json:"shuffle"`
		Repeat   string      `json:"repeat"`
	}
	err = json.Unmarshal([]byte(out), &state)
	return state.Queue, state.PlayNext, state.Shuffle, state.Repeat, err
}

func formatMediaItems(items []MediaItem) string {
	if len(items) == 0 {
		return "queue is empty"
	}
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = fmt.Sprintf("%3d  %-8s  %s", i+1, item.Kind, item.display())
	}
	return strings.Join(lines, "\n")
}

func runQueueCommand(ctx context.Context, args []string) (string, error) {
	jsonOutput := false
	words := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else {
			words = append(words, arg)
		}
	}
	action := "list"
	if len(words) > 0 {
		action, words = strings.ToLower(words[0]), words[1:]
	}
	switch action {
	case "help", "--help", "-h":
		return `Usage: chill queue [command] [--json]

Commands:
  list                         show pending media
  search <words>               search pending media
  add <source...>              append files, folders, playlists, or URLs
  next <source...>             add media to the play-next lane
  replace <source...>          replace pending media
  play <number>                play a pending item now
  move <from> <to>             reorder pending media
  remove <number>              remove a pending item
  undo                         restore the previous queue edit
  clear                        clear pending media`, nil
	case "list", "search":
		queue, playNext, shuffle, repeat, err := queueState()
		if err != nil {
			return "", err
		}
		items := append(cloneItems(playNext), queue...)
		if action == "search" {
			if len(words) == 0 {
				return "", fmt.Errorf("usage: chill queue search <words>")
			}
			query := strings.ToLower(strings.Join(words, " "))
			filtered := items[:0]
			for _, item := range items {
				if strings.Contains(strings.ToLower(item.display()+" "+item.Source), query) {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		if jsonOutput {
			data, err := json.MarshalIndent(struct {
				Items    []MediaItem `json:"items"`
				PlayNext int         `json:"play_next"`
				Shuffle  bool        `json:"shuffle"`
				Repeat   string      `json:"repeat"`
			}{items, len(playNext), shuffle, repeat}, "", "  ")
			return string(data), err
		}
		return formatMediaItems(items), nil
	case "add", "next", "replace":
		if len(words) == 0 {
			return "", fmt.Errorf("queue %s needs a file, folder, playlist, or URL", action)
		}
		items, err := loadMediaInputs(ctx, words)
		if err != nil {
			return "", err
		}
		command := map[string]string{"add": "queue-append", "next": "queue-next", "replace": "queue-replace"}[action]
		return sendItems(command, items)
	case "clear", "undo":
		if len(words) != 0 {
			return "", fmt.Errorf("usage: chill queue %s", action)
		}
		if err := ensureDaemon(); err != nil {
			return "", err
		}
		return ask("queue-" + action)
	case "remove":
		if len(words) != 1 {
			return "", fmt.Errorf("usage: chill queue remove <number>")
		}
		if err := ensureDaemon(); err != nil {
			return "", err
		}
		return ask("queue-remove " + words[0])
	case "move":
		if len(words) != 2 {
			return "", fmt.Errorf("usage: chill queue move <number> <number>")
		}
		from, err1 := strconv.Atoi(words[0])
		to, err2 := strconv.Atoi(words[1])
		if err1 != nil || err2 != nil {
			return "", fmt.Errorf("queue positions must be numbers")
		}
		data, _ := json.Marshal(queueRequest{Index: from, To: to})
		if err := ensureDaemon(); err != nil {
			return "", err
		}
		return ask("queue-move " + string(data))
	case "play":
		if len(words) != 1 {
			return "", fmt.Errorf("usage: chill queue play <number>")
		}
		index, err := strconv.Atoi(words[0])
		queue, playNext, _, _, stateErr := queueState()
		items := append(playNext, queue...)
		if err != nil || stateErr != nil || index < 1 || index > len(items) {
			if stateErr != nil {
				return "", stateErr
			}
			return "", fmt.Errorf("queue index is out of range")
		}
		if err := ensureDaemon(); err != nil {
			return "", err
		}
		return ask(fmt.Sprintf("queue-play %d", index))
	default:
		return "", fmt.Errorf("unknown queue command %q", action)
	}
}

func playlistNames(library *libraryState) []string {
	names := make([]string, 0, len(library.Playlists))
	for _, playlist := range library.Playlists {
		names = append(names, playlist.Name)
	}
	sort.Strings(names)
	return names
}

func mutatePlaylist(action string, request playlistMutation) (string, error) {
	if err := ensureDaemon(); err != nil {
		return "", err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	return ask(action + " " + string(data))
}

func writePlaylistFile(playlist savedPlaylist, format, destination string) (string, error) {
	var body strings.Builder
	if format == "pls" {
		body.WriteString("[playlist]\n")
		for i, item := range playlist.Items {
			length := int(item.Duration)
			if item.Kind == MediaStation {
				length = -1
			}
			fmt.Fprintf(&body, "File%d=%s\nTitle%d=%s\nLength%d=%d\n", i+1, item.Source, i+1, item.Title, i+1, length)
		}
		fmt.Fprintf(&body, "NumberOfEntries=%d\nVersion=2\n", len(playlist.Items))
	} else {
		body.WriteString("#EXTM3U\n")
		for _, item := range playlist.Items {
			length := int(item.Duration)
			if item.Kind == MediaStation {
				length = -1
			}
			fmt.Fprintf(&body, "#EXTINF:%d,%s\n%s\n", length, item.Title, item.Source)
		}
	}
	if destination == "" || destination == "-" {
		return strings.TrimSuffix(body.String(), "\n"), nil
	}
	if err := os.WriteFile(destination, []byte(body.String()), 0600); err != nil {
		return "", err
	}
	return "exported: " + destination, nil
}

func runPlaylistCommand(ctx context.Context, args []string) (string, error) {
	jsonOutput, foreground, output, format, importName := false, false, "", "", ""
	var words []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--fg":
			foreground = true
		case "-o", "--output":
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s needs a path", args[i-1])
			}
			output = args[i]
		case "--format":
			i++
			if i >= len(args) {
				return "", fmt.Errorf("--format needs m3u, m3u8, or pls")
			}
			format = strings.ToLower(args[i])
		case "--name":
			i++
			if i >= len(args) {
				return "", fmt.Errorf("--name needs a playlist name")
			}
			importName = args[i]
		default:
			words = append(words, args[i])
		}
	}
	action := "list"
	if len(words) > 0 {
		action, words = strings.ToLower(words[0]), words[1:]
	}
	library, err := fetchLibraryState()
	if err != nil {
		return "", err
	}
	switch action {
	case "help", "--help", "-h":
		return `Usage: chill playlist [command] [options]

Commands:
  list                         list saved playlists
  show <name>                  list playlist items
  play <name> [--fg]           play a saved playlist
  create <name> [source...]    create or replace a playlist
  add <name> <source...>       append media
  save <name>                  save the pending queue
  move <name> <from> <to>      reorder an item
  remove <name> <number>       remove an item
  dedupe <name>                remove repeated media
  rename <old> <new>           rename a playlist
  delete <name>                delete a playlist
  import <path> [--name name]  import M3U, M3U8, or PLS
  export <name> [-o path]      export M3U, M3U8, or PLS`, nil
	case "list":
		names := playlistNames(library)
		if jsonOutput {
			data, err := json.MarshalIndent(library.Playlists, "", "  ")
			return string(data), err
		}
		if len(names) == 0 {
			return "no saved playlists", nil
		}
		var lines []string
		for _, name := range names {
			p := library.Playlists[playlistKey(name)]
			lines = append(lines, fmt.Sprintf("%-24s %d item(s)", p.Name, len(p.Items)))
		}
		return strings.Join(lines, "\n"), nil
	case "show", "play":
		if len(words) != 1 {
			return "", fmt.Errorf("usage: chill playlist %s <name>", action)
		}
		playlist, ok := library.Playlists[playlistKey(words[0])]
		if !ok {
			return "", fmt.Errorf("playlist not found: %s", words[0])
		}
		if action == "play" {
			if foreground {
				return "", runForegroundMediaItems(playlist.Items)
			}
			return playMediaItems(playlist.Items)
		}
		if jsonOutput {
			data, err := json.MarshalIndent(playlist, "", "  ")
			return string(data), err
		}
		return formatMediaItems(playlist.Items), nil
	case "create", "add":
		if len(words) == 0 {
			return "", fmt.Errorf("usage: chill playlist %s <name> [files...]", action)
		}
		name, inputs := words[0], words[1:]
		items := []MediaItem{}
		if len(inputs) > 0 {
			items, err = loadMediaInputs(ctx, inputs)
			if err != nil {
				return "", err
			}
		}
		if existing, ok := library.Playlists[playlistKey(name)]; action == "add" && ok {
			items = append(existing.Items, items...)
		} else if action == "add" {
			return "", fmt.Errorf("playlist not found: %s", name)
		}
		return mutatePlaylist("playlist-put", playlistMutation{Name: name, Items: items})
	case "save":
		if len(words) != 1 {
			return "", fmt.Errorf("usage: chill playlist save <name>")
		}
		queue, playNext, _, _, err := queueState()
		if err != nil {
			return "", err
		}
		return mutatePlaylist("playlist-put", playlistMutation{Name: words[0], Items: append(playNext, queue...)})
	case "delete":
		if len(words) != 1 {
			return "", fmt.Errorf("usage: chill playlist delete <name>")
		}
		return mutatePlaylist("playlist-delete", playlistMutation{Name: words[0]})
	case "remove":
		if len(words) != 2 {
			return "", fmt.Errorf("usage: chill playlist remove <name> <number>")
		}
		playlist, ok := library.Playlists[playlistKey(words[0])]
		index, parseErr := strconv.Atoi(words[1])
		if !ok {
			return "", fmt.Errorf("playlist not found: %s", words[0])
		}
		if parseErr != nil || index < 1 || index > len(playlist.Items) {
			return "", fmt.Errorf("playlist item number is out of range")
		}
		playlist.Items = slices.Delete(playlist.Items, index-1, index)
		return mutatePlaylist("playlist-put", playlistMutation{Name: playlist.Name, Items: playlist.Items})
	case "move":
		if len(words) != 3 {
			return "", fmt.Errorf("usage: chill playlist move <name> <from> <to>")
		}
		playlist, ok := library.Playlists[playlistKey(words[0])]
		from, firstErr := strconv.Atoi(words[1])
		to, secondErr := strconv.Atoi(words[2])
		if !ok {
			return "", fmt.Errorf("playlist not found: %s", words[0])
		}
		if firstErr != nil || secondErr != nil || from < 1 || from > len(playlist.Items) || to < 1 || to > len(playlist.Items) {
			return "", fmt.Errorf("playlist item number is out of range")
		}
		item := playlist.Items[from-1]
		playlist.Items = slices.Delete(playlist.Items, from-1, from)
		playlist.Items = slices.Insert(playlist.Items, to-1, item)
		return mutatePlaylist("playlist-put", playlistMutation{Name: playlist.Name, Items: playlist.Items})
	case "dedupe":
		if len(words) != 1 {
			return "", fmt.Errorf("usage: chill playlist dedupe <name>")
		}
		playlist, ok := library.Playlists[playlistKey(words[0])]
		if !ok {
			return "", fmt.Errorf("playlist not found: %s", words[0])
		}
		seen := map[string]bool{}
		items := playlist.Items[:0]
		for _, item := range playlist.Items {
			if !seen[item.ID] {
				seen[item.ID] = true
				items = append(items, item)
			}
		}
		return mutatePlaylist("playlist-put", playlistMutation{Name: playlist.Name, Items: items})
	case "rename":
		if len(words) != 2 {
			return "", fmt.Errorf("usage: chill playlist rename <old> <new>")
		}
		return mutatePlaylist("playlist-rename", playlistMutation{Name: words[0], NewName: words[1]})
	case "import":
		if len(words) != 1 {
			return "", fmt.Errorf("usage: chill playlist import <m3u|pls> [--name name]")
		}
		items, err := readPlaylist(ctx, words[0])
		if err != nil {
			return "", err
		}
		name := importName
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(words[0]), filepath.Ext(words[0]))
		}
		return mutatePlaylist("playlist-put", playlistMutation{Name: name, Items: items})
	case "export":
		if len(words) != 1 {
			return "", fmt.Errorf("usage: chill playlist export <name> [-o path] [--format m3u|m3u8|pls]")
		}
		playlist, ok := library.Playlists[playlistKey(words[0])]
		if !ok {
			return "", fmt.Errorf("playlist not found: %s", words[0])
		}
		if format == "" {
			format = "m3u"
			if strings.EqualFold(filepath.Ext(output), ".pls") {
				format = "pls"
			} else if strings.EqualFold(filepath.Ext(output), ".m3u8") {
				format = "m3u8"
			}
		}
		if format != "m3u" && format != "m3u8" && format != "pls" {
			return "", fmt.Errorf("format must be m3u, m3u8, or pls")
		}
		return writePlaylistFile(playlist, format, output)
	default:
		return "", fmt.Errorf("unknown playlist command %q", action)
	}
}

func libraryCollection(library *libraryState, name string) ([]MediaItem, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "recent" {
		return cloneItems(library.Recent), nil
	}
	var selected map[string]bool
	var order []string
	if name == "favorites" {
		selected, order = library.Favorites, library.FavoriteOrder
	} else if name == "bookmarks" {
		selected, order = library.Bookmarks, library.BookmarkOrder
	} else {
		return nil, fmt.Errorf("collection must be recent, favorites, or bookmarks")
	}
	known := map[string]MediaItem{}
	for _, item := range library.knownItems() {
		known[item.ID] = item
	}
	items := make([]MediaItem, 0, len(selected))
	seen := map[string]bool{}
	for _, id := range order {
		if item, ok := known[id]; ok && selected[id] && !seen[id] {
			items = append(items, item)
			seen[id] = true
		}
	}
	// Older library files had no explicit order. Keep any recoverable marked
	// items visible even if the state has not yet been rewritten.
	for _, item := range library.knownItems() {
		if selected[item.ID] && !seen[item.ID] {
			items = append(items, item)
			seen[item.ID] = true
		}
	}
	return items, nil
}

func runLibraryCommand(args []string) (string, error) {
	jsonOutput := false
	var words []string
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
		} else {
			words = append(words, arg)
		}
	}
	library, err := fetchLibraryState()
	if err != nil {
		return "", err
	}
	if len(words) == 0 {
		result := map[string]int{"queue": len(library.Queue) + len(library.PlayNext), "playlists": len(library.Playlists), "favorites": len(library.Favorites), "bookmarks": len(library.Bookmarks), "recent": len(library.Recent)}
		if jsonOutput {
			data, marshalErr := json.MarshalIndent(result, "", "  ")
			return string(data), marshalErr
		}
		return fmt.Sprintf("queue %d · playlists %d · favorites %d · bookmarks %d · recent %d", result["queue"], result["playlists"], result["favorites"], result["bookmarks"], result["recent"]), nil
	}
	action := strings.ToLower(words[0])
	if action == "help" || action == "--help" || action == "-h" {
		return `Usage: chill library [collection] [--json]

Collections:
  recent                       recently played media
  favorites                    favorite media
  bookmarks                    bookmarked media

Actions:
  play <collection> <number>   play an item
  add <collection> <number>    append an item to the queue
  next <collection> <number>   put an item next`, nil
	}
	if action == "recent" || action == "favorites" || action == "bookmarks" {
		if len(words) != 1 {
			return "", fmt.Errorf("usage: chill library %s [--json]", action)
		}
		items, err := libraryCollection(library, action)
		if err != nil {
			return "", err
		}
		if jsonOutput {
			data, marshalErr := json.MarshalIndent(items, "", "  ")
			return string(data), marshalErr
		}
		if len(items) == 0 {
			return "no " + action, nil
		}
		return formatMediaItems(items), nil
	}
	if action != "play" && action != "add" && action != "next" || len(words) != 3 {
		return "", fmt.Errorf("usage: chill library [recent|favorites|bookmarks] or chill library play|add|next <collection> <number>")
	}
	items, err := libraryCollection(library, words[1])
	if err != nil {
		return "", err
	}
	index, err := strconv.Atoi(words[2])
	if err != nil || index < 1 || index > len(items) {
		return "", fmt.Errorf("library item number is out of range")
	}
	if action == "play" {
		return playMedia(items[index-1], false)
	}
	command := "queue-append"
	if action == "next" {
		command = "queue-next"
	}
	return sendItems(command, []MediaItem{items[index-1]})
}
