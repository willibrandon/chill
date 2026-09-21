// Command icy serves a quiet PCM stream with deterministic metadata for VHS.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	address     = "127.0.0.1:18765"
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

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: go run ./vhs/fixtures <start|serve|stop>")
	}
	switch os.Args[1] {
	case "start":
		return start()
	case "serve":
		server := newServer()
		err := server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
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
