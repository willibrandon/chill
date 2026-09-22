// Command fixtures supplies deterministic media and isolated Chill sessions for VHS.
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	address     = "127.0.0.1:18765"
	mediaPath   = "vhs/fixtures/showcase.m4a"
	sampleRate  = 48_000
	channels    = 2
	sampleWidth = 2
	interval    = 32_768
	streamTitle = "StreamTitle='Traditional - Auld Lang Syne';"
	healthText  = "chill-vhs-icy"
)

var (
	header   = wavHeader()
	samples  = tone()
	metadata = icyMetadata()
)

func wavHeader() []byte {
	result := make([]byte, 44)
	copy(result[0:4], "RIFF")
	binary.LittleEndian.PutUint32(result[4:8], 0x7fffffff)
	copy(result[8:12], "WAVE")
	copy(result[12:16], "fmt ")
	binary.LittleEndian.PutUint32(result[16:20], 16)
	binary.LittleEndian.PutUint16(result[20:22], 1)
	binary.LittleEndian.PutUint16(result[22:24], channels)
	binary.LittleEndian.PutUint32(result[24:28], sampleRate)
	binary.LittleEndian.PutUint32(result[28:32], sampleRate*channels*sampleWidth)
	binary.LittleEndian.PutUint16(result[32:34], channels*sampleWidth)
	binary.LittleEndian.PutUint16(result[34:36], sampleWidth*8)
	copy(result[36:40], "data")
	binary.LittleEndian.PutUint32(result[40:44], 0x7fffffff-36)
	return result
}

func tone() []byte {
	result := make([]byte, sampleRate*channels*sampleWidth)
	for sample := range sampleRate {
		value := int16(900 * math.Sin(2*math.Pi*220*float64(sample)/sampleRate))
		offset := sample * channels * sampleWidth
		binary.LittleEndian.PutUint16(result[offset:offset+2], uint16(value))
		binary.LittleEndian.PutUint16(result[offset+2:offset+4], uint16(value))
	}
	return result
}

func icyMetadata() []byte {
	blocks := (len(streamTitle) + 15) / 16
	result := make([]byte, 1+blocks*16)
	result[0] = byte(blocks)
	copy(result[1:], streamTitle)
	return result
}

func serveStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("icy-metaint", "32768")
	w.Header().Set("icy-name", "Chill Showcase")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	prefix, offset := header, 0
	delay := time.Second * time.Duration(interval) / time.Duration(sampleRate*channels*sampleWidth)
	for {
		chunk := make([]byte, 0, interval)
		if len(prefix) > 0 {
			count := min(interval, len(prefix))
			chunk = append(chunk, prefix[:count]...)
			prefix = prefix[count:]
		}
		for len(chunk) < interval {
			count := min(interval-len(chunk), len(samples)-offset)
			chunk = append(chunk, samples[offset:offset+count]...)
			offset = (offset + count) % len(samples)
		}
		if _, err := w.Write(chunk); err != nil {
			return
		}
		if _, err := w.Write(metadata); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(delay):
		}
	}
}

func newServer() *http.Server {
	var server *http.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, healthText)
	})
	mux.HandleFunc("/stream", serveStream)
	mux.HandleFunc("/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		go func() {
			time.Sleep(50 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		}()
	})
	server = &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	return server
}

func healthy() bool {
	client := &http.Client{Timeout: 250 * time.Millisecond}
	response, err := client.Get("http://" + address + "/health")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64))
	return err == nil && response.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == healthText
}

func start() error {
	if healthy() {
		fmt.Println("fixture ready")
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(os.TempDir(), "chill-vhs-icy.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	command := exec.Command(executable, "serve")
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		logFile.Close()
		return err
	}
	_ = logFile.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if healthy() {
			_ = command.Process.Release()
			fmt.Println("fixture ready")
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = command.Process.Kill()
	return fmt.Errorf("fixture did not start; see %s", filepath.Join(os.TempDir(), "chill-vhs-icy.log"))
}

func stop() error {
	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/shutdown", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		if !healthy() {
			fmt.Println("fixture stopped")
			return nil
		}
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("fixture returned %s", response.Status)
	}
	deadline := time.Now().Add(2 * time.Second)
	for healthy() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	_ = os.Remove(filepath.Join(os.TempDir(), "chill-vhs-icy.log"))
	fmt.Println("fixture stopped")
	return nil
}

func isolatedEnvironment(root string) ([]string, string) {
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	runtimeDirectory := filepath.Join(root, "runtime")
	cache := filepath.Join(root, "cache")
	overrides := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + config,
		"APPDATA=" + config,
		"LOCALAPPDATA=" + cache,
		"TMPDIR=" + runtimeDirectory,
		"TMP=" + runtimeDirectory,
		"TEMP=" + runtimeDirectory,
		"CLICOLOR_FORCE=1",
		"COLORTERM=truecolor",
	}
	replaced := map[string]bool{"HOME": true, "USERPROFILE": true, "XDG_CONFIG_HOME": true, "APPDATA": true, "LOCALAPPDATA": true, "TMPDIR": true, "TMP": true, "TEMP": true, "NO_COLOR": true, "CLICOLOR_FORCE": true, "COLORTERM": true}
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && !replaced[strings.ToUpper(name)] {
			environment = append(environment, entry)
		}
	}
	return append(environment, overrides...), home
}

func writeOverviewInterface(root, home string) error {
	config := filepath.Join(root, "config")
	if runtime.GOOS == "darwin" {
		config = filepath.Join(home, "Library", "Application Support")
	}
	path := filepath.Join(config, "chill", "interface.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data := []byte("{\n  \"visualizer_height\": 10,\n  \"panels\": {\n    \"source\": false,\n    \"queue\": false,\n    \"equalizer\": false,\n    \"audio\": false,\n    \"downloads\": false,\n    \"network\": false,\n    \"metadata\": false\n  }\n}\n")
	return os.WriteFile(path, data, 0600)
}

func buildChill(root string) (string, error) {
	name := "chill-vhs"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(root, name)
	command := exec.Command("go", "build", "-o", path, ".")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return "", err
	}
	return path, nil
}

func stopChill(path string, environment []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, "--stop")
	command.Env = environment
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	_ = command.Run()
}

func repl(overview, withMedia bool) error {
	if withMedia {
		defer clean()
		if err := media(); err != nil {
			return err
		}
	}
	root, err := os.MkdirTemp("", "chill-vhs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	environment, home := isolatedEnvironment(root)
	for _, directory := range []string{home, filepath.Join(root, "config"), filepath.Join(root, "cache"), filepath.Join(root, "runtime")} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return err
		}
	}
	if overview {
		if err := writeOverviewInterface(root, home); err != nil {
			return err
		}
	}
	binary, err := buildChill(root)
	if err != nil {
		return err
	}
	defer stopChill(binary, environment)
	command := exec.Command(binary, "--theme", "Midnight", "-i")
	command.Env = environment
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func media() error {
	command := exec.Command(
		"ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=220:sample_rate=48000",
		"-t", "120", "-c:a", "aac", "-b:a", "96k",
		"-metadata", "artist=Traditional", "-metadata", "title=Auld Lang Syne", "-metadata", "album=Chill Showcase",
		"-metadata", "lyrics=Should auld acquaintance be forgot\nAnd never brought to mind?\nShould auld acquaintance be forgot\nAnd days of auld lang syne?",
		"-y", mediaPath,
	)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func clean() {
	if err := os.Remove(mediaPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(os.Stderr, "remove media:", err)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: go run ./vhs/fixtures <repl|repl-media|repl-overview|start|serve|stop>")
	}
	switch os.Args[1] {
	case "repl":
		return repl(false, false)
	case "repl-media":
		return repl(false, true)
	case "repl-overview":
		return repl(true, false)
	case "start":
		return start()
	case "serve":
		server := newServer()
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case "stop":
		return stop()
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}

}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
