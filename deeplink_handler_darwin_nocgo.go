//go:build darwin && !cgo

package main

func runDeepLinkHandlerIfNeeded() bool { return false }

func nativeDeepLinkHandlerAvailable() bool { return false }
