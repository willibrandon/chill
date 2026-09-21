//go:build !darwin && !linux && !windows

package main

import "runtime"

func registerDeepLinks() (string, error) {
	return "chill:// registration is not available on " + runtime.GOOS, nil
}

func unregisterDeepLinks() (string, error) {
	return "chill:// registration is not available on " + runtime.GOOS, nil
}

func deepLinkRegistrationStatus() (bool, string) { return false, "unsupported on " + runtime.GOOS }
