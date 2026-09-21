//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework AppKit -framework Foundation

#include <stdlib.h>

extern void chillHandleURLEvent(char *rawURL);
void chillRunURLHandler(void);
*/
import "C"

import (
	"context"
	"os"
	"path/filepath"
)

//export chillHandleURLEvent
func chillHandleURLEvent(rawURL *C.char) {
	if rawURL == nil {
		return
	}
	_, _ = runDeepLink(context.Background(), C.GoString(rawURL), false)
}

func runDeepLinkHandlerIfNeeded() bool {
	if len(os.Args) != 1 || filepath.Base(os.Args[0]) != "chill-url-handler" {
		return false
	}
	C.chillRunURLHandler()
	return true
}

func nativeDeepLinkHandlerAvailable() bool { return true }
