package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func updateArchive(t *testing.T, member string, binary []byte, zipped bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	if zipped {
		archive := zip.NewWriter(&buf)
		file, err := archive.Create(member)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(binary); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&buf)
		archive := tar.NewWriter(gz)
		if err := archive.WriteHeader(&tar.Header{Name: member, Mode: 0755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(binary); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func updateServer(t *testing.T, goos string, binary []byte, problem string) *httptest.Server {
	t.Helper()
	base := "chill_9.0.0_" + goos + "_" + runtime.GOARCH
	name, member := base+".tar.gz", base+"/chill"
	if goos == "windows" {
		name, member = base+".zip", base+"/chill.exe"
	}
	if problem == "missing executable" {
		member = "../unexpected"
	}
	payload := updateArchive(t, member, binary, goos == "windows")
	sum := sha256.Sum256(payload)
	checksums := fmt.Sprintf("%x  %s\n", sum, name)
	if problem == "checksum mismatch" {
		payload = append(payload, 'x')
	}
	if problem == "missing checksum" {
		checksums = ""
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			if problem == "api failure" {
				http.Error(w, "try later", http.StatusServiceUnavailable)
				return
			}
			release := releaseInfo{Tag: "v9.0.0", Assets: []releaseAsset{
				{Name: name, URL: "http://" + r.Host + "/archive"},
				{Name: "checksums.txt", URL: "http://" + r.Host + "/checksums"},
			}}
			if problem == "missing asset" {
				release.Assets = nil
			}
			if problem == "prerelease" {
				release.Prerelease = true
			}
			json.NewEncoder(w).Encode(release)
		case "/checksums":
			fmt.Fprint(w, checksums)
		case "/archive":
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestReleaseUpdateInstallsArchive(t *testing.T) {
	for _, platform := range []string{"linux", "darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "chill")
			if err := os.WriteFile(target, []byte("previous executable"), 0755); err != nil {
				t.Fatal(err)
			}
			server := updateServer(t, platform, []byte("updated executable"), "")
			u := releaseUpdater{client: server.Client(), url: server.URL + "/latest", goos: platform, goarch: runtime.GOARCH}
			version, changed, err := u.install(target, "v0.4.1")
			if err != nil || !changed || version != "v9.0.0" {
				t.Fatalf("version=%s changed=%v err=%v", version, changed, err)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "updated executable" {
				t.Fatalf("installed %q: %v", data, err)
			}
		})
	}
}

func TestFailedUpdatePreservesExecutable(t *testing.T) {
	for _, problem := range []string{"api failure", "checksum mismatch", "missing checksum", "missing executable", "missing asset", "prerelease"} {
		t.Run(problem, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "chill")
			if err := os.WriteFile(target, []byte("previous executable"), 0755); err != nil {
				t.Fatal(err)
			}
			server := updateServer(t, runtime.GOOS, []byte("updated executable"), problem)
			u := releaseUpdater{client: server.Client(), url: server.URL + "/latest", goos: runtime.GOOS, goarch: runtime.GOARCH}
			if _, changed, err := u.install(target, "v0.4.1"); err == nil || changed {
				t.Fatalf("bad update succeeded: changed=%v err=%v", changed, err)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "previous executable" {
				t.Fatalf("failed update changed installation: %q, %v", data, err)
			}
		})
	}
}

func TestUpdateNeverDowngradesOrReinstalls(t *testing.T) {
	server := updateServer(t, runtime.GOOS, []byte("unexpected update"), "")
	u := releaseUpdater{client: server.Client(), url: server.URL + "/latest", goos: runtime.GOOS, goarch: runtime.GOARCH}
	for _, version := range []string{"v9.0.0", "v10.0.0"} {
		target := filepath.Join(t.TempDir(), "chill")
		if err := os.WriteFile(target, []byte("keep this"), 0755); err != nil {
			t.Fatal(err)
		}
		got, changed, err := u.install(target, version)
		if err != nil || changed || got != version {
			t.Fatalf("version=%s changed=%v err=%v", got, changed, err)
		}
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "keep this" {
			t.Fatalf("unchanged installation overwritten: %q, %v", data, err)
		}
	}
}

func TestUpdateRunningExecutable(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "chill.exe")
	if err := os.WriteFile(target, binary, 0755); err != nil {
		t.Fatal(err)
	}
	server := updateServer(t, runtime.GOOS, binary, "")
	cmd := exec.Command(target, "-test.run=^TestUpdateHelperProcess$")
	cmd.Env = append(os.Environ(), "CHILL_UPDATE_HELPER="+server.URL+"/latest")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("updating running executable: %v\n%s", err, out)
	}
	cmd = exec.Command(target, "-test.run=^TestUpdateHelperProcess$")
	cmd.Env = append(os.Environ(), "CHILL_UPDATE_HELPER=check")
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "replacement runs") {
		t.Fatalf("running replacement: %v\n%s", err, out)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".chill.exe.old-*"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("old update backups were not cleaned up: %v, %v", backups, err)
	}
}

func TestUpdateHelperProcess(t *testing.T) {
	url := os.Getenv("CHILL_UPDATE_HELPER")
	if url == "" {
		return // invoked with its environment by TestUpdateRunningExecutable
	}
	cleanupUpdateBackups()
	if url == "check" {
		fmt.Println("replacement runs")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u := releaseUpdater{client: &http.Client{Timeout: time.Minute}, url: url, goos: runtime.GOOS, goarch: runtime.GOARCH}
	if _, changed, err := u.install(exe, "v0.4.1"); err != nil || !changed {
		t.Fatalf("self update changed=%v err=%v", changed, err)
	}
}
