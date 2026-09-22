package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

type doctorOptions struct {
	stations  bool
	stream    string
	timeout   time.Duration
	logs      bool
	providers bool
	audio     bool
}

func parseDoctorOptions(args []string, out io.Writer) (doctorOptions, error) {
	var options doctorOptions
	flags := flag.NewFlagSet("chill doctor", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = func() { printDoctorHelp(flags) }
	flags.BoolVar(&options.stations, "stations", false, "resolve every configured station without playing audio")
	flags.StringVar(&options.stream, "stream", "", "decode one station, URL, or file without playing audio")
	flags.DurationVar(&options.timeout, "timeout", 45*time.Second, "timeout per stream check")
	flags.BoolVar(&options.audio, "audio", false, "open the selected output briefly without audible audio")
	flags.BoolVar(&options.logs, "logs", false, "include the most recent daemon startup log")
	flags.BoolVar(&options.providers, "providers", false, "validate enabled provider credentials and connections")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 || options.stations && options.stream != "" || options.timeout <= 0 {
		return options, fmt.Errorf("usage: chill doctor [--stations | --stream <station-or-url>] [--timeout 45s] [--logs]")
	}
	return options, nil
}

type doctorReport struct {
	out      io.Writer
	passed   int
	warnings int
	failed   int
}

func (r *doctorReport) check(level, name, message string) {
	switch level {
	case "OK":
		r.passed++
	case "WARN":
		r.warnings++
	case "FAIL":
		r.failed++
	}
	fmt.Fprintf(r.out, "[%s] %s: %s\n", level, name, message)
}

func runDoctor(args []string, out io.Writer) error {
	return runDoctorContext(context.Background(), args, out)
}

func runDoctorContext(ctx context.Context, args []string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	options, err := parseDoctorOptions(args, out)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	r := doctorReport{out: out}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe = resolvedExecutable(exe)
	r.check("OK", "client", fmt.Sprintf("chill %s, protocol %d (%s)", buildVersion(), daemonProtocol, exe))

	r.check("OK", "native playback", "MP3, FLAC, PCM WAV, and Ogg Vorbis are built in")
	if _, err := loadToolSettings(); err != nil {
		r.check("FAIL", "tool settings", err.Error())
	}
	for _, tool := range []struct{ name, capability string }{{"ffmpeg", "additional formats, HLS, and website playback"}, {"yt-dlp", "YouTube and supported websites"}, {"ffprobe", "additional metadata and duration probing"}} {
		path, err := toolPath(tool.name)
		if err != nil {
			if _, invalid := errors.AsType[*toolConfigError](err); invalid {
				r.check("FAIL", tool.name, err.Error())
			} else {
				r.check("WARN", tool.name, tool.capability+" unavailable: "+err.Error()+"; install with `"+strings.Join(installCommands([]string{tool.name}), "` then `")+"`")
			}
		} else {
			r.program(ctx, tool.name, path)
			if tool.name == "yt-dlp" && path == legacyExtractorPath() {
				settings, _ := loadToolSettings()
				if settings.YTDLP == "" {
					r.check("WARN", "yt-dlp location", "using a legacy portable location; set yt_dlp in tools.json or put the executable on PATH")
				}
			}
		}
	}
	runtime, path, runtimeErr := selectedJSRuntime()
	if runtimeErr != nil {
		r.check("FAIL", "YouTube runtime", runtimeErr.Error())
	} else if path == "" {
		r.check("WARN", "YouTube runtime", "no JavaScript runtime found; install Deno or select Node or QuickJS in tools.json")
	} else {
		r.program(ctx, runtime, path)
		r.check("OK", "YouTube setup", "runtime detected; use --stream with a YouTube URL to verify extraction and EJS components")
	}

	settings, settingsErr := loadPlaybackSettings()
	if settingsErr != nil {
		r.check("FAIL", "audio settings", settingsErr.Error())

	} else {
		deviceCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		devices, deviceErr := listAudioDevices(deviceCtx, settings.Audio.Device)
		cancel()
		if deviceErr != nil {
			r.check("WARN", "audio devices", deviceErr.Error())
		} else {
			selected := settings.Audio.Device == "auto" || slices.ContainsFunc(devices, func(device AudioDevice) bool { return device.ID == settings.Audio.Device })
			level := "OK"
			message := fmt.Sprintf("%d outputs; %s", len(devices), formatAudioSettings(settings.Audio))
			if !selected {
				level, message = "WARN", message+"; selected device is disconnected and playback will use the system default"
			}
			r.check(level, "audio devices", message)
		}
	}
	if options.audio && settingsErr == nil {
		output, err := openAudioOutput(outputSettings(settings.Audio), 0, true, true)
		if err != nil {
			r.check("FAIL", "audio output", err.Error())
		} else {
			info := output.Info()
			output.Close()
			mode := "shared"
			if info.Exclusive {
				mode = "exclusive"
			}
			r.check("OK", "audio output", fmt.Sprintf("%s output opened successfully; PCM %d Hz, device %d Hz, %s", info.Backend, settings.Audio.SampleRate, info.SampleRate, mode))
		}
	}
	registered, registration := deepLinkRegistrationStatus()
	if registered {
		r.check("OK", "chill links", registration)
	} else {
		r.check("WARN", "chill links", "not registered; run chill link register")
	}
	if options.providers {
		registry, providerErr := providers()
		if providerErr != nil {
			r.check("FAIL", "providers", providerErr.Error())
		} else {
			for _, info := range registry.list(ctx, true) {
				level, message := "OK", strings.Join(info.Capabilities, ", ")
				if !info.Configured {
					level, message = "FAIL", info.Error
				}
				r.check(level, "provider "+info.Key, message)
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkDoctorConfig(); err != nil {
		r.check("FAIL", "config", err.Error()+"; fix stations.json, then rerun doctor")
	} else {
		r.check("OK", "config", fmt.Sprintf("%s (%d stations, default %s; missing file uses built-ins)", configPath(), len(stationSnapshot()), defaultStation()))
	}

	if library, err := loadPodcastLibrary(); err != nil {
		r.check("FAIL", "podcasts", err.Error()+"; check "+podcastPath())
	} else {
		r.check("OK", "podcasts", fmt.Sprintf("%d subscriptions, chart country %s", len(library.Subscriptions), library.Country))
	}

	s, daemonErr := inspectDaemonContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if daemonErr != nil {
		r.check("FAIL", "daemon", daemonErr.Error()+"; try chill --stop then chill; if no daemon is alive, a playback command replaces stale discovery data")
	} else {
		compatibility, message := daemonCompatibility(s, buildVersion(), buildIdentity())
		level := "OK"
		if compatibility == "incompatible" {
			level = "FAIL"
		} else if compatibility != "current" && compatibility != "not-running" {
			level = "WARN"
		}
		if compatibility == "incompatible" || compatibility == "client-outdated" {
			update := packageUpdateCommand(exe)
			if update == "" {
				update = "chill update"
			}
			message += "; update the client with `" + update + "`"
		}
		if s != nil {
			version := s.Version
			if version == "" {
				version = "unknown"
			}
			message = fmt.Sprintf("chill %s, protocol %d, state %s; %s", version, s.Protocol, s.State, message)
		}
		r.check(level, "daemon", message)
		if s != nil && s.Error != "" {
			level = "WARN"
			if s.State == "failed" {
				level = "FAIL"
			}
			r.check(level, "playback", s.Error+"; check this station with chill doctor --stream "+s.Station)
		}
	}
	fmt.Fprintln(out, "Daemon startup log:", daemonLogPath())
	if options.logs || daemonErr != nil {
		if log := readDaemonLog(); log != "" {
			fmt.Fprintln(out, "Most recent startup (may be from an earlier run):\n"+log)
		}
	}

	var checks []Station
	if options.stations {
		checks = stationSnapshot()
	} else if options.stream != "" {
		if station := findStation(options.stream); station != nil {
			checks = []Station{*station}
		} else if strings.HasPrefix(options.stream, "https://") || strings.HasPrefix(options.stream, "http://") {
			checks = []Station{{Name: "stream", URL: options.stream}}
		} else if info, err := os.Stat(options.stream); err == nil && !info.IsDir() {
			checks = []Station{{Name: "file", URL: options.stream}}
		} else {
			return fmt.Errorf("unknown source %q; use a station, HTTP(S) URL, or local file", options.stream)
		}
	}
	for _, station := range checks {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintf(out, "Checking %s (%s)...\n", station.Name, station.URL)
		info, err := probeStreamContext(ctx, "", station.URL, options.timeout)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			r.check("FAIL", station.Name, err.Error())
			continue
		}
		r.check("OK", station.Name, info.Title+"; initial audio decoded successfully")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintf(out, "Doctor complete. Passed: %d, warnings: %d, failed: %d.\n", r.passed, r.warnings, r.failed)
	if r.failed > 0 {
		return fmt.Errorf("doctor found problems; see the checks above")
	}
	return nil
}

func (r *doctorReport) program(ctx context.Context, name, path string) {
	if ctx.Err() != nil {
		return
	}
	if path == "" {
		r.check("WARN", name, "not found; install with `"+strings.Join(installCommands([]string{name}), "` then `")+"`")
		return
	}
	flag := "--version"
	if name == "ffmpeg" || name == "ffprobe" {
		flag = "-version"
	} else if name == "quickjs" {
		flag = "-h"
	}
	stdout, stderr, err := diagnosticCommandContext(ctx, path, 5*time.Second, flag)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		r.check("WARN", name, fmt.Sprintf("%s: %v; %s", path, err, stderr))
		return
	}
	version := strings.SplitN(firstNonempty(stdout, stderr), "\n", 2)[0]
	r.check("OK", name, version+" ("+resolvedExecutable(path)+")")
	if name == "yt-dlp" {
		if date, err := time.Parse("2006.01.02", version); err == nil && time.Since(date) > 90*24*time.Hour {
			r.check("WARN", name, "extractor is over 90 days old; update it using the package manager that installed it (YouTube changes frequently)")
		}
	}
}

func checkDoctorConfig() error {
	if configPath() == "" {
		return fmt.Errorf("no user config directory")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	for _, station := range cfg.Stations {
		name := strings.TrimSpace(station.Name)
		if name == "" || strings.ContainsAny(name, " \t\r\n") || strings.TrimSpace(station.URL) == "" || strings.ContainsAny(station.URL, "\r\n") {
			return fmt.Errorf("invalid station %q: names must be one word and URLs must be nonempty and one line", station.Name)
		}
	}
	return loadUserStations()
}

func diagnosticCommand(path string, timeout time.Duration, args ...string) (string, string, error) {
	return diagnosticCommandContext(context.Background(), path, timeout, args...)
}

func diagnosticCommandContext(ctx context.Context, path string, timeout time.Duration, args ...string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	cmd := exec.Command(path, args...)
	var stdout, stderr tailBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	tree, err := startInTree(cmd)
	if err != nil {
		return stdout.String(), stderr.String(), err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err = <-exited:
		tree.kill()
	case <-timer.C:
		tree.kill() // includes an extractor's JavaScript runtime or launcher
		<-exited
		err = fmt.Errorf("timed out after %s", timeout)
	case <-ctx.Done():
		tree.kill()
		<-exited
		err = ctx.Err()
	}
	return stdout.String(), stderr.String(), err
}

type streamInfo struct {
	// Title is the extractor's display title for the stream.
	Title string `json:"title"`
	// LiveStatus identifies whether the source is currently live.
	LiveStatus string `json:"live_status"`
}

func probeStreamContext(ctx context.Context, _ string, source string, timeout time.Duration) (streamInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	settings, err := loadPlaybackSettings()
	if err != nil {
		return streamInfo{}, err
	}
	reader, _, err := openPCM(ctx, source, 0, false, settings.Audio, func(string) {})
	if err != nil {
		return streamInfo{}, err
	}
	defer reader.Close()
	var block [4096]byte
	n, err := io.ReadFull(reader, block[:])
	if n < 8 || err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return streamInfo{}, fmt.Errorf("decode source: %w", err)
	}
	return streamInfo{Title: source}, nil
}
