package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/willibrandon/chill/internal/tracklog"
)

func runHistoryCommand(args []string, colored bool) (string, error) {
	limit, jsonOutput, clear := 50, false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "clear":
			clear = true
		case "--limit":
			i++
			if i >= len(args) {
				return "", fmt.Errorf("--limit needs a number")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 || n > 200 {
				return "", fmt.Errorf("limit must be between 0 and 200")
			}
			limit = n
		case "--help", "-h", "help":
			text := "Usage: chill history [--limit 0-200] [--json]\n       chill history clear"
			if colored {
				text = dim + text + reset
			}
			return text, nil
		default:
			return "", fmt.Errorf("unknown history option %s", args[i])
		}
	}
	store := tracklog.DefaultStore()
	if clear {
		if limit != 50 {
			return "", fmt.Errorf("usage: chill history clear")
		}
		if err := store.Clear(); err != nil {
			return "", err
		}
		if jsonOutput {
			return "[]", nil
		}
		return "track history cleared", nil
	}
	entries, err := store.Load(limit)
	if err != nil {
		return "", err
	}
	if jsonOutput {
		data, err := json.Marshal(entries)
		return string(data), err
	}
	if len(entries) == 0 {
		return "no track history", nil
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		title := entry.Title
		if entry.Artist != "" {
			title = entry.Artist + " — " + entry.Title
		}
		lines = append(lines, fmt.Sprintf("%-12s  %s  %s", relativeTime(entry.PlayedAt), entry.Station, title))
	}
	return strings.Join(lines, "\n"), nil
}

func relativeTime(value time.Time) string {
	delta := time.Since(value)
	if delta < 0 {
		delta = 0
	}
	switch {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm ago", int(delta.Minutes()))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(delta.Hours()))
	case delta < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(delta.Hours()/24))
	default:
		return value.Local().Format("2006-01-02")
	}
}
