package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const latestReleaseURL = "https://api.github.com/repos/willibrandon/chill/releases/latest"
const maxUpdateSize = 64 << 20

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type releaseInfo struct {
	Tag        string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type releaseUpdater struct {
	client       *http.Client
	url          string
	goos, goarch string
}

func updateChill() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	u := releaseUpdater{client: &http.Client{Timeout: 2 * time.Minute}, url: latestReleaseURL, goos: runtime.GOOS, goarch: runtime.GOARCH}
	version, changed, err := u.install(exe, buildVersion())
	if err != nil {
		return "", err
	}
	// Run the installed client so its version handshake also updates the daemon.
	// The updater itself is still executing the old binary at this point.
	if isDaemonRunning() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, exe, "--status").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("chill %s is installed, but updating the daemon failed: %v\n%s", version, err, strings.TrimSpace(string(out)))
		}
	}
	if !changed {
		return "already up to date: chill " + version, nil
	}
	return "updated to chill " + version, nil
}

func (u releaseUpdater) fetch(url string, limit int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "chill-updater")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download exceeds size limit: %s", url)
	}
	return data, nil
}

func (u releaseUpdater) install(target, current string) (string, bool, error) {
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", false, err
	}
	unlock, err := lockFile(target + ".update.lock")
	if err != nil {
		return "", false, fmt.Errorf("cannot update %s: %w", target, err)
	}
	defer unlock()
	// A different client may already have updated this installation while we
	// waited. Compare the on-disk executable, not just this process's version.
	if info, err := buildinfo.ReadFile(target); err == nil && semver.IsValid(info.Main.Version) {
		current = info.Main.Version
	}
	data, err := u.fetch(u.url, 1<<20)
	if err != nil {
		return "", false, err
	}
	var release releaseInfo
	if err := json.Unmarshal(data, &release); err != nil {
		return "", false, fmt.Errorf("reading latest release: %w", err)
	}
	if !semver.IsValid(release.Tag) || release.Draft || release.Prerelease || semver.Prerelease(release.Tag) != "" {
		return "", false, fmt.Errorf("GitHub did not return a stable release")
	}
	if semver.IsValid(current) && semver.Compare(current, release.Tag) >= 0 {
		return current, false, nil
	}
	base := fmt.Sprintf("chill_%s_%s_%s", strings.TrimPrefix(release.Tag, "v"), u.goos, u.goarch)
	archive, member := base+".tar.gz", base+"/chill"
	if u.goos == "windows" {
		archive, member = base+".zip", base+"/chill.exe"
	}
	var archiveURL, checksumsURL string
	for _, asset := range release.Assets {
		switch asset.Name {
		case archive:
			archiveURL = asset.URL
		case "checksums.txt":
			checksumsURL = asset.URL
		}
	}
	if archiveURL == "" || checksumsURL == "" {
		return "", false, fmt.Errorf("release %s has no complete download for %s/%s", release.Tag, u.goos, u.goarch)
	}
	checksums, err := u.fetch(checksumsURL, 64<<10)
	if err != nil {
		return "", false, err
	}
	payload, err := u.fetch(archiveURL, maxUpdateSize)
	if err != nil {
		return "", false, err
	}
	if err := verifyUpdateChecksum(payload, checksums, archive); err != nil {
		return "", false, err
	}
	binary, err := extractUpdate(payload, member, u.goos == "windows")
	if err != nil {
		return "", false, err
	}
	staged, err := os.CreateTemp(filepath.Dir(target), ".chill-update-*")
	if err != nil {
		return "", false, fmt.Errorf("cannot update %s: %w", target, err)
	}
	defer os.Remove(staged.Name())
	_, writeErr := staged.Write(binary)
	if writeErr == nil {
		writeErr = staged.Chmod(0755)
	}
	if writeErr == nil {
		writeErr = staged.Sync()
	}
	closeErr := staged.Close()
	if writeErr != nil {
		return "", false, writeErr
	}
	if closeErr != nil {
		return "", false, closeErr
	}
	if err := replaceUpdate(staged.Name(), target); err != nil {
		return "", false, fmt.Errorf("replacing %s: %w", target, err)
	}
	return release.Tag, true, nil
}

func verifyUpdateChecksum(payload, checksums []byte, name string) error {
	sum := sha256.Sum256(payload)
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		expected, err := hex.DecodeString(fields[0])
		if err != nil || !bytes.Equal(sum[:], expected) {
			return fmt.Errorf("checksum mismatch for %s; installed binary was not changed", name)
		}
		return nil
	}
	return fmt.Errorf("no checksum for %s; installed binary was not changed", name)
}

func extractUpdate(payload []byte, member string, zipped bool) ([]byte, error) {
	var reader io.Reader
	if zipped {
		archive, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
		if err != nil {
			return nil, err
		}
		for _, file := range archive.File {
			if file.Name != member || !file.Mode().IsRegular() {
				continue
			}
			f, err := file.Open()
			if err != nil {
				return nil, err
			}
			defer f.Close()
			reader = f
			break
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		archive := tar.NewReader(io.LimitReader(gz, maxUpdateSize+1))
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if header.Name == member && header.Typeflag == tar.TypeReg {
				reader = archive
				break
			}
		}
	}
	if reader == nil {
		return nil, fmt.Errorf("release archive does not contain %s", member)
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxUpdateSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxUpdateSize {
		return nil, fmt.Errorf("invalid executable size in release archive")
	}
	return data, nil
}
