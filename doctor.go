package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type doctorOptions struct {
	stations bool
	stream   string
	timeout  time.Duration
	logs     bool
}

func parseDoctorOptions(args []string, out io.Writer) (doctorOptions, error) {
	var options doctorOptions
	flags := flag.NewFlagSet("chill doctor", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.BoolVar(&options.stations, "stations", false, "resolve every configured station without playing audio")
	flags.StringVar(&options.stream, "stream", "", "resolve one station name or URL without playing audio")
	flags.DurationVar(&options.timeout, "timeout", 45*time.Second, "timeout per stream check")
	flags.BoolVar(&options.logs, "logs", false, "include the most recent daemon startup log")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 || options.stations && options.stream != "" || options.timeout <= 0 {
		return options, fmt.Errorf("usage: chill doctor [--stations | --stream <station-or-url>] [--timeout 45s] [--logs]")
	}
	return options, nil
}

type doctorReport struct {
	out    io.Writer
	failed bool
}

func (r *doctorReport) check(level, name, message string) {
	r.failed = r.failed || level == "FAIL"
	fmt.Fprintf(r.out, "[%s] %s: %s\n", level, name, message)
}

func runDoctor(args []string, out io.Writer) error {
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

	mpv, _ := exec.LookPath("mpv")
	extractor := findYtdl(mpv)
	r.program("mpv", mpv)
	r.program("yt-dlp", extractor)
	if deno, _ := exec.LookPath("deno"); deno != "" {
		r.program("deno", deno)
	} else {
		r.check("WARN", "YouTube runtime", "Deno not found; current yt-dlp needs a JavaScript runtime for full YouTube support. Install deno or configure another supported runtime in yt-dlp")
	}

	if err := checkDoctorConfig(); err != nil {
		r.check("FAIL", "config", err.Error()+"; fix stations.json, then rerun doctor")
	} else {
		r.check("OK", "config", fmt.Sprintf("%s (%d stations, default %s; missing file uses built-ins)", configPath(), len(stationSnapshot()), defaultStation()))
	}

	s, daemonErr := inspectDaemon()
	if daemonErr != nil {
		r.check("FAIL", "daemon", daemonErr.Error()+"; try chill --stop then chill; if no daemon is alive, a playback command replaces stale discovery data")
	} else {
		compatibility, message := daemonCompatibility(s, buildVersion())
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
		} else {
			return fmt.Errorf("unknown station %q; use a configured station name or an HTTP(S) URL", options.stream)
		}
	}
	if len(checks) > 0 && extractor == "" {
		r.check("FAIL", "streams", "cannot resolve streams until yt-dlp is installed")
	} else {
		for _, station := range checks {
			fmt.Fprintf(out, "Checking %s (%s)...\n", station.Name, station.URL)
			info, err := probeStream(extractor, station.URL, options.timeout)
			if err != nil {
				r.check("FAIL", station.Name, err.Error()+"; update yt-dlp, check your network, or replace the station URL with chill add")
				continue
			}
			level := "OK"
			if info.LiveStatus != "is_live" {
				level = "WARN"
			}
			r.check(level, station.Name, fmt.Sprintf("%s (%s; stream resolves)", info.Title, info.LiveStatus))
		}
	}
	if r.failed {
		return fmt.Errorf("doctor found problems; see the checks above")
	}
	return nil
}

func (r *doctorReport) program(name, path string) {
	if path == "" {
		r.check("FAIL", name, "not found; install with `"+strings.Join(installCommands([]string{name}), "` then `")+"`")
		return
	}
	stdout, stderr, err := diagnosticCommand(path, 5*time.Second, "--version")
	if err != nil {
		r.check("FAIL", name, fmt.Sprintf("%s: %v; %s", path, err, stderr))
		return
	}
	version := strings.SplitN(stdout, "\n", 2)[0]
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
	}
	return stdout.String(), stderr.String(), err
}

type streamInfo struct {
	Title      string `json:"title"`
	LiveStatus string `json:"live_status"`
}

func probeStream(extractor, url string, timeout time.Duration) (streamInfo, error) {
	stdout, stderr, err := diagnosticCommand(extractor, timeout,
		"--ignore-config", "--no-playlist", "--simulate", "--no-progress",
		"--socket-timeout", "10", "--retries", "0", "--format", "bestaudio/best",
		"--print", `{"title":%(title)j,"live_status":%(live_status)j}`, "--", url)
	if err != nil {
		return streamInfo{}, fmt.Errorf("stream resolution: %w\n%s", err, stderr)
	}
	var info streamInfo
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		return info, fmt.Errorf("reading extractor result: %w", err)
	}
	if info.Title == "" {
		return info, fmt.Errorf("extractor returned no stream title")
	}
	if info.LiveStatus == "" {
		info.LiveStatus = "unknown live status"
	}
	return info, nil
}
