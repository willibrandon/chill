//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

const deepLinkRegistryKey = `Software\Classes\chill`

func registerDeepLinks() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, deepLinkRegistryKey, registry.SET_VALUE|registry.CREATE_SUB_KEY)
	if err != nil {
		return "", err
	}
	defer key.Close()
	if err := key.SetStringValue("", "URL:chill Link"); err != nil {
		return "", err
	}
	if err := key.SetStringValue("URL Protocol", ""); err != nil {
		return "", err
	}
	command, _, err := registry.CreateKey(key, `shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return "", err
	}
	defer command.Close()
	if err := command.SetStringValue("", fmt.Sprintf(`"%s" "%%1"`, executable)); err != nil {
		return "", err
	}
	return "registered chill:// links", nil
}

func unregisterDeepLinks() (string, error) {
	for _, path := range []string{deepLinkRegistryKey + `\shell\open\command`, deepLinkRegistryKey + `\shell\open`, deepLinkRegistryKey + `\shell`, deepLinkRegistryKey} {
		if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil && err != registry.ErrNotExist {
			return "", err
		}
	}
	return "unregistered chill:// links", nil
}

func deepLinkRegistrationStatus() (bool, string) {
	key, err := registry.OpenKey(registry.CURRENT_USER, deepLinkRegistryKey, registry.QUERY_VALUE)
	if err != nil {
		return false, deepLinkRegistryKey
	}
	key.Close()
	return true, deepLinkRegistryKey
}
