package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
)

const ipcNamespaceEnvironment = "CHILL_IPC_NAMESPACE"

// ipcNamespaceSuffix isolates internal sessions without placing untrusted
// environment text directly in a socket path or Windows named-pipe name.
func ipcNamespaceSuffix() string {
	namespace := os.Getenv(ipcNamespaceEnvironment)
	if namespace == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(namespace))
	return "-" + hex.EncodeToString(digest[:8])
}
