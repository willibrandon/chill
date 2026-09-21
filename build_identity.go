package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sync"
)

var executableIdentity = sync.OnceValue(func() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
})

// buildIdentity distinguishes rebuilt executables with the same semantic version.
func buildIdentity() string {
	return executableIdentity()
}
