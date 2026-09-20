//go:build !windows

package main

// enableANSI does nothing, Unix terminals handle ANSI escape sequences natively.
func enableANSI() {}
