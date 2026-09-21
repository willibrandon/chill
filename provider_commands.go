package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type providerCommandOptions struct {
	provider   string
	limit      int
	offset     int
	jsonOutput bool
	foreground bool
	action     string
	selection  string
}

func runProvidersCommand(ctx context.Context, args []string, jsonOutput bool) (string, error) {
	validate := false
	for _, arg := range args {
		switch arg {
		case "--check":
			validate = true
		case "--json":
			jsonOutput = true
		case "help", "--help", "-h":
			return `Usage: chill providers [--check] [--json]

Lists enabled providers and their capabilities. --check validates configured credentials and server connectivity.`, nil
		default:
			return "", fmt.Errorf("unknown providers option %q", arg)
		}
	}
	registry, err := providers()
	if err != nil {
		return "", err
	}
	infos := registry.list(ctx, validate)
	if jsonOutput {
		data, err := json.Marshal(infos, json.Deterministic(true), jsontext.WithIndent("  "))
		return string(data), err
	}
	if len(infos) == 0 {
		return "no providers are enabled; run chill setup", nil
	}
	lines := make([]string, 0, len(infos))
	for _, info := range infos {
		state := "ready"
		if !info.Configured {
			state = info.Error
		}
		lines = append(lines, fmt.Sprintf("%-16s %-20s %s", info.Key, strings.Join(info.Capabilities, ", "), state))
	}
	return strings.Join(lines, "\n"), nil
}

func runSearchCommand(ctx context.Context, args []string, inheritedJSON, inheritedForeground bool) (string, error) {
	if helpRequested(args) {
		return "Usage: chill search <words> [--provider name] [--limit N] [--play N|--queue N|all|--next N] [--json] [--fg]", nil
	}
	options, words, err := parseProviderCommandOptions(args)
	if err != nil {
		return "", err
	}
	options.jsonOutput = options.jsonOutput || inheritedJSON
	options.foreground = options.foreground || inheritedForeground
	if len(words) == 0 {
		return "", errors.New("usage: chill search <words> [--provider name] [--limit N] [--play N|--queue N|all|--next N] [--json] [--fg]")
	}
	registry, err := providers()
	if err != nil {
		return "", err
	}
	items, err := registry.search(ctx, options.provider, strings.Join(words, " "), options.limit, nil)
	if err != nil {
		return "", err
	}
	return consumeProviderResults(items, options)
}

func runBrowseCommand(ctx context.Context, args []string, inheritedJSON, inheritedForeground bool) (string, error) {
	if helpRequested(args) {
		return "Usage: chill browse <provider> [kind] [id] [--limit N] [--offset N] [--play N|--queue N|all|--next N] [--json] [--fg]", nil
	}
	options, words, err := parseProviderCommandOptions(args)
	if err != nil {
		return "", err
	}
	options.jsonOutput = options.jsonOutput || inheritedJSON
	options.foreground = options.foreground || inheritedForeground
	if len(words) == 0 || len(words) > 3 {
		return "", errors.New("usage: chill browse <provider> [kind] [id] [--limit N] [--offset N] [--play N|--queue N|all|--next N] [--json] [--fg]")
	}
	request := providerBrowseRequest{Limit: options.limit, Offset: options.offset}
	if len(words) > 1 {
		request.Kind = words[1]
	}
	if len(words) > 2 {
		request.ID = words[2]
	}
	registry, err := providers()
	if err != nil {
		return "", err
	}
	page, err := registry.browse(ctx, words[0], request)
	if err != nil {
		return "", err
	}
	if options.jsonOutput && options.action == "" {
		data, err := json.Marshal(page, json.Deterministic(true), jsontext.WithIndent("  "))
		return string(data), err
	}
	if options.action == "" && len(page.Entries) > 0 {
		lines := make([]string, len(page.Entries))
		for i, entry := range page.Entries {
			detail := cleanProviderText(entry.Subtitle)
			if entry.Count > 0 {
				detail = strings.TrimSpace(fmt.Sprintf("%s  %d item(s)", detail, entry.Count))
			}
			if entry.ID != "" {
				detail = strings.TrimSpace(detail + "  id=" + strconv.Quote(entry.ID))
			}
			lines[i] = fmt.Sprintf("%3d  %-12s  %-36s  %s", i+1, entry.Kind, cleanProviderText(entry.Title), detail)
		}
		return strings.Join(lines, "\n"), nil
	}
	return consumeProviderResults(page.Items, options)
}

func parseProviderCommandOptions(args []string) (providerCommandOptions, []string, error) {
	options := providerCommandOptions{provider: "all", limit: 20}
	var words []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "--provider":
			selected, err := value()
			if err != nil {
				return options, nil, err
			}
			options.provider = strings.ToLower(selected)
		case "--limit":
			raw, err := value()
			if err != nil {
				return options, nil, err
			}
			options.limit, err = strconv.Atoi(raw)
			if err != nil || options.limit < 1 || options.limit > 100 {
				return options, nil, errors.New("--limit must be between 1 and 100")
			}
		case "--offset":
			raw, err := value()
			if err != nil {
				return options, nil, err
			}
			options.offset, err = strconv.Atoi(raw)
			if err != nil || options.offset < 0 {
				return options, nil, errors.New("--offset must be a non-negative number")
			}
		case "--play", "--queue", "--next":
			if options.action != "" {
				return options, nil, errors.New("choose only one of --play, --queue, or --next")
			}
			selected, err := value()
			if err != nil {
				return options, nil, err
			}
			options.action, options.selection = strings.TrimPrefix(arg, "--"), selected
		case "--json":
			options.jsonOutput = true
		case "--fg":
			options.foreground = true
		default:
			if strings.HasPrefix(arg, "-") {
				return options, nil, fmt.Errorf("unknown search option %q", arg)
			}
			words = append(words, arg)
		}
	}
	return options, words, nil
}

func consumeProviderResults(items []MediaItem, options providerCommandOptions) (string, error) {
	if options.action == "" {
		if options.jsonOutput {
			data, err := json.Marshal(items, json.Deterministic(true), jsontext.WithIndent("  "))
			return string(data), err
		}
		if len(items) == 0 {
			return "no results", nil
		}
		lines := make([]string, len(items))
		for i, item := range items {
			lines[i] = fmt.Sprintf("%3d  %-12s  %s", i+1, item.Provider, item.display())
		}
		return strings.Join(lines, "\n"), nil
	}
	selected, err := selectProviderResults(items, options.selection, true)
	if err != nil {
		return "", err
	}
	for _, item := range selected {
		if err := providerPlaybackAvailable(item); err != nil {
			return "", err
		}
	}
	if options.foreground {
		if options.action != "play" {
			return "", errors.New("--fg can only be combined with --play")
		}
		return "", runForegroundMediaItems(selected)
	}
	switch options.action {
	case "play":
		return playMediaItems(selected)
	case "queue":
		return sendItems("queue-append", selected)
	case "next":
		return sendItems("queue-next", selected)
	default:
		return "", errors.New("invalid provider result action")
	}
}

func providerPlaybackAvailable(item MediaItem) error {
	if item.Kind == MediaProvider && item.ProviderMeta["playback"] == "catalog" {
		return fmt.Errorf("%s is catalog-only and has no playable preview", item.display())
	}
	return nil
}

func selectProviderResults(items []MediaItem, selection string, allowAll bool) ([]MediaItem, error) {
	if len(items) == 0 {
		return nil, errors.New("no provider results")
	}
	if allowAll && strings.EqualFold(selection, "all") {
		return items, nil
	}
	index, err := strconv.Atoi(selection)
	if err != nil || index < 1 || index > len(items) {
		return nil, fmt.Errorf("result number must be between 1 and %d", len(items))
	}
	return []MediaItem{items[index-1]}, nil
}
