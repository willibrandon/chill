package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type resolvedAudio struct {
	// URL is the resolved media address consumed by FFmpeg.
	URL string `json:"url"`
	// Headers contains the extractor's required request headers.
	Headers map[string]string `json:"http_headers"`
	// Artwork is a safely cached provider image for the active item.
	Artwork string `json:"artwork,omitempty"`
}

var (
	findExtractor        = findYtdl
	runDiagnosticCommand = diagnosticCommandContext
)

func resolveAudio(ctx context.Context, source string) (resolvedAudio, error) {
	return resolveAudioWithCookies(ctx, source, "")
}

func resolveAudioWithCookies(ctx context.Context, source, cookiesFrom string) (resolvedAudio, error) {
	if err := ctx.Err(); err != nil {
		return resolvedAudio{}, err
	}
	u, err := url.Parse(source)
	if err != nil {
		// Local filenames can contain percent signs that are not URL escapes.
		if !strings.Contains(source, "://") {
			return resolvedAudio{URL: source}, nil
		}
		return resolvedAudio{}, err
	}
	if u.Scheme == "chill-provider" {
		return resolveProviderAudio(ctx, source)
	}
	// Local media and direct radio URLs do not need extraction. YouTube page
	// URLs do; yt-dlp supplies the signed URL and required HTTP headers together.
	if !sourceNeedsYtdl(source) {
		return resolvedAudio{URL: source}, nil
	}
	extractor := findExtractor("")
	if extractor == "" {
		return resolvedAudio{}, &requirementsError{missing: []string{"yt-dlp"}}
	}
	args, err := extractorArgs()
	if err != nil {
		return resolvedAudio{}, err
	}
	args = append(args, "--format", extractorAudioFormat(source), "--print", `{"url":%(url)j,"http_headers":%(http_headers)j}`)
	if cookiesFrom != "" {
		args = append(args, "--cookies-from-browser", cookiesFrom)
	}
	args = append(args, "--", source)
	stdout, stderr, err := runDiagnosticCommand(ctx, extractor, 35*time.Second, args...)
	if err != nil {
		return resolvedAudio{}, fmt.Errorf("stream resolution: %w; %s", err, stderr)
	}
	var resolved resolvedAudio
	if err := json.Unmarshal([]byte(stdout), &resolved); err != nil {
		return resolvedAudio{}, fmt.Errorf("stream resolution: %w", err)
	}
	if resolved.URL == "" {
		return resolvedAudio{}, fmt.Errorf("extractor returned no audio URL")
	}
	return resolved, nil
}

func extractorAudioFormat(source string) string {
	u, err := url.Parse(source)
	if err == nil {
		host := strings.ToLower(u.Hostname())
		if host == "mixcloud.com" || strings.HasSuffix(host, ".mixcloud.com") {
			return "bestaudio[protocol=https]/bestaudio[protocol=http]/bestaudio[protocol=m3u8_native]/bestaudio[protocol=m3u8]/bestaudio/best"
		}
	}
	return "bestaudio/best"
}
