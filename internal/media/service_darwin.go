//go:build darwin && cgo

package media

/*
#cgo CFLAGS: -x objective-c -fblocks
#cgo LDFLAGS: -framework AppKit -framework Foundation -framework MediaPlayer

#include <stdint.h>
#include <stdlib.h>

#import <AppKit/AppKit.h>
#import <MediaPlayer/MediaPlayer.h>

extern void chillMediaCommand(uintptr_t handle, int command, double value);

typedef struct ChillMediaBridge {
	uintptr_t handle;
	id toggleTarget;
	id playTarget;
	id pauseTarget;
	id stopTarget;
	id nextTarget;
	id previousTarget;
	id positionTarget;
} ChillMediaBridge;

static NSString *chillArtworkURL = nil;
static NSImage *chillArtworkImage = nil;

static void chillMediaResetArtwork(void) {
	[chillArtworkURL release];
	[chillArtworkImage release];
	chillArtworkURL = nil;
	chillArtworkImage = nil;
}

static void chillMediaInitApp(void) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
}

static void chillMediaClear(void) {
	MPRemoteCommandCenter *center = [MPRemoteCommandCenter sharedCommandCenter];
	center.changePlaybackPositionCommand.enabled = NO;
	[MPNowPlayingInfoCenter defaultCenter].nowPlayingInfo = nil;
	[MPNowPlayingInfoCenter defaultCenter].playbackState = MPNowPlayingPlaybackStateStopped;
	chillMediaResetArtwork();
}

static void *chillMediaCreate(uintptr_t handle) {
	ChillMediaBridge *bridge = (ChillMediaBridge *)calloc(1, sizeof(ChillMediaBridge));
	if (!bridge) return NULL;
	bridge->handle = handle;
	MPRemoteCommandCenter *center = [MPRemoteCommandCenter sharedCommandCenter];
	bridge->toggleTarget = [center.togglePlayPauseCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
		chillMediaCommand(handle, 0, 0); return MPRemoteCommandHandlerStatusSuccess;
	}];
	bridge->playTarget = [center.playCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
		chillMediaCommand(handle, 1, 0); return MPRemoteCommandHandlerStatusSuccess;
	}];
	bridge->pauseTarget = [center.pauseCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
		chillMediaCommand(handle, 2, 0); return MPRemoteCommandHandlerStatusSuccess;
	}];
	bridge->stopTarget = [center.stopCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
		chillMediaCommand(handle, 3, 0); return MPRemoteCommandHandlerStatusSuccess;
	}];
	bridge->nextTarget = [center.nextTrackCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
		chillMediaCommand(handle, 4, 0); return MPRemoteCommandHandlerStatusSuccess;
	}];
	bridge->previousTarget = [center.previousTrackCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
		chillMediaCommand(handle, 5, 0); return MPRemoteCommandHandlerStatusSuccess;
	}];
	bridge->positionTarget = [center.changePlaybackPositionCommand addTargetWithHandler:^MPRemoteCommandHandlerStatus(MPRemoteCommandEvent *event) {
		MPChangePlaybackPositionCommandEvent *position = (MPChangePlaybackPositionCommandEvent *)event;
		chillMediaCommand(handle, 7, position.positionTime); return MPRemoteCommandHandlerStatusSuccess;
	}];
	return bridge;
}

static void chillMediaDestroy(void *raw) {
	ChillMediaBridge *bridge = (ChillMediaBridge *)raw;
	if (!bridge) { chillMediaClear(); return; }
	MPRemoteCommandCenter *center = [MPRemoteCommandCenter sharedCommandCenter];
	if (bridge->toggleTarget) [center.togglePlayPauseCommand removeTarget:bridge->toggleTarget];
	if (bridge->playTarget) [center.playCommand removeTarget:bridge->playTarget];
	if (bridge->pauseTarget) [center.pauseCommand removeTarget:bridge->pauseTarget];
	if (bridge->stopTarget) [center.stopCommand removeTarget:bridge->stopTarget];
	if (bridge->nextTarget) [center.nextTrackCommand removeTarget:bridge->nextTarget];
	if (bridge->previousTarget) [center.previousTrackCommand removeTarget:bridge->previousTarget];
	if (bridge->positionTarget) [center.changePlaybackPositionCommand removeTarget:bridge->positionTarget];
	chillMediaClear();
	free(bridge);
}

static void chillMediaUpdate(const char *title, const char *artist, const char *album,
	const char *sourceURL, const char *genre, const char *artURL,
	double duration, double elapsed, int status, int seekable,
	int canNext, int canPrevious) {
	@autoreleasepool {
		MPRemoteCommandCenter *center = [MPRemoteCommandCenter sharedCommandCenter];
		center.changePlaybackPositionCommand.enabled = seekable ? YES : NO;
		center.nextTrackCommand.enabled = canNext ? YES : NO;
		center.previousTrackCommand.enabled = canPrevious ? YES : NO;
		if (status == 0) { chillMediaClear(); return; }
		NSMutableDictionary *info = [NSMutableDictionary dictionary];
		if (title) info[MPMediaItemPropertyTitle] = [NSString stringWithUTF8String:title];
		if (artist) info[MPMediaItemPropertyArtist] = [NSString stringWithUTF8String:artist];
		if (album) info[MPMediaItemPropertyAlbumTitle] = [NSString stringWithUTF8String:album];
		if (sourceURL) info[MPNowPlayingInfoPropertyExternalContentIdentifier] = [NSString stringWithUTF8String:sourceURL];
		if (genre) info[MPMediaItemPropertyGenre] = [NSString stringWithUTF8String:genre];
		if (duration > 0) info[MPMediaItemPropertyPlaybackDuration] = @(duration);
		if (elapsed >= 0) info[MPNowPlayingInfoPropertyElapsedPlaybackTime] = @(elapsed);
		info[MPNowPlayingInfoPropertyPlaybackRate] = @(status == 1 ? 1.0 : 0.0);
		NSString *requestedArt = artURL ? [NSString stringWithUTF8String:artURL] : nil;
		if ((requestedArt || chillArtworkURL) && ![chillArtworkURL isEqualToString:requestedArt]) {
			chillMediaResetArtwork();
			NSURL *url = requestedArt ? [NSURL URLWithString:requestedArt] : nil;
			if (url && url.isFileURL) {
				chillArtworkURL = [requestedArt copy];
				chillArtworkImage = [[NSImage alloc] initWithContentsOfURL:url];
			}
		}
		if (chillArtworkImage) {
			NSImage *image = chillArtworkImage;
			MPMediaItemArtwork *art = [[[MPMediaItemArtwork alloc] initWithBoundsSize:image.size
					requestHandler:^NSImage * _Nonnull(CGSize size) { return image; }] autorelease];
				info[MPMediaItemPropertyArtwork] = art;
		}
		[MPNowPlayingInfoCenter defaultCenter].nowPlayingInfo = info;
		[MPNowPlayingInfoCenter defaultCenter].playbackState = status == 1
			? MPNowPlayingPlaybackStatePlaying : MPNowPlayingPlaybackStatePaused;
	}
}

static void chillMediaTick(void) {
	[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode
		beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
}
*/
import "C"

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/cgo"
	"strings"
	"sync"
	"time"
	"unsafe"
)

type updateRequest struct {
	state State
}

// Service owns the system now-playing session.
type Service struct {
	send         func(Command)
	handle       cgo.Handle
	updates      chan updateRequest
	mu           sync.Mutex
	bridge       unsafe.Pointer
	runLoopOwned bool
	closed       bool
	artwork      map[string]string
	artworkBusy  map[string]bool
	latest       State
}

var (
	activeService   *Service
	activeServiceMu sync.Mutex
)

func init() { runtime.LockOSThread() }

// New creates a system now-playing session.
func New(send func(Command)) (*Service, error) {
	activeServiceMu.Lock()
	defer activeServiceMu.Unlock()
	if activeService != nil {
		return nil, fmt.Errorf("system media session is already active")
	}
	s := &Service{send: send, updates: make(chan updateRequest, 1), artwork: make(map[string]string), artworkBusy: make(map[string]bool)}
	s.handle = cgo.NewHandle(s)
	activeService = s
	return s, nil
}

func serviceForHandle(raw uintptr) (service *Service) {
	if raw == 0 {
		return nil
	}
	defer func() {
		if recover() != nil {
			service = nil
		}
	}()
	service, _ = cgo.Handle(raw).Value().(*Service)
	return service
}

//export chillMediaCommand
func chillMediaCommand(handle C.uintptr_t, command C.int, value C.double) {
	service := serviceForHandle(uintptr(handle))
	if service == nil {
		return
	}
	kind := CommandKind(command)
	request := Command{Kind: kind}
	if kind == SetPosition {
		request.Position = time.Duration(float64(value) * float64(time.Second))
	}
	service.send(request)
}

// Run executes work while pumping the native application run loop.
func Run(service *Service, work func() error) error {
	if service == nil {
		return work()
	}
	type result struct{ err error }
	resultChannel := make(chan result, 1)
	done := make(chan struct{})
	go func() { resultChannel <- result{work()}; close(done) }()
	service.run(done)
	return (<-resultChannel).err
}

func (s *Service) run(done <-chan struct{}) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-done
		return
	}
	s.runLoopOwned = true
	handle := s.handle
	s.mu.Unlock()
	C.chillMediaInitApp()
	bridge := C.chillMediaCreate(C.uintptr_t(handle))
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		C.chillMediaDestroy(bridge)
		return
	}
	s.bridge = bridge
	s.mu.Unlock()
	for {
		select {
		case request := <-s.updates:
			applyUpdate(request.state)
		default:
		}
		C.chillMediaTick()
		select {
		case <-done:
			s.release(true)
			return
		default:
		}
	}
}

func boolInt(value bool) C.int {
	if value {
		return 1
	}
	return 0
}

func cString(value string) (*C.char, func()) {
	if value == "" {
		return nil, func() {}
	}
	text := C.CString(value)
	return text, func() { C.free(unsafe.Pointer(text)) }
}

func applyUpdate(state State) {
	title, freeTitle := cString(state.Track.Title)
	defer freeTitle()
	artist, freeArtist := cString(state.Track.Artist)
	defer freeArtist()
	album, freeAlbum := cString(state.Track.Album)
	defer freeAlbum()
	source, freeSource := cString(state.Track.URL)
	defer freeSource()
	genre, freeGenre := cString(state.Track.Genre)
	defer freeGenre()
	art, freeArt := cString(state.Track.ArtURL)
	defer freeArt()
	status := C.int(0)
	if state.Status == StatusPlaying {
		status = 1
	} else if state.Status == StatusPaused {
		status = 2
	}
	C.chillMediaUpdate(title, artist, album, source, genre, art, C.double(state.Track.Duration.Seconds()),
		C.double(state.Position.Seconds()), status, boolInt(state.Seekable),
		boolInt(state.CanGoNext), boolInt(state.CanGoPrevious))
}

// Update publishes a complete playback snapshot.
func (s *Service) Update(state State) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.latest = state
	rawArtwork := state.Track.ArtURL
	if parsed, err := url.Parse(rawArtwork); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		cached, known := s.artwork[rawArtwork]
		state.Track.ArtURL = cached
		if !known && !s.artworkBusy[rawArtwork] {
			s.artworkBusy[rawArtwork] = true
			go s.downloadArtwork(rawArtwork)
		}
	}
	s.mu.Unlock()
	request := updateRequest{state: state}
	select {
	case s.updates <- request:
	default:
		select {
		case <-s.updates:
		default:
		}
		s.updates <- request
	}
}

func (s *Service) downloadArtwork(rawURL string) {
	sum := sha256.Sum256([]byte(rawURL))
	directory, err := os.UserCacheDir()
	if err == nil {
		directory = filepath.Join(directory, "chill", "artwork")
		err = os.MkdirAll(directory, 0700)
	}
	path := filepath.Join(directory, hex.EncodeToString(sum[:])+".image")
	if err == nil {
		if _, statErr := os.Stat(path); statErr != nil {
			client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 5 || request.URL.Scheme != "http" && request.URL.Scheme != "https" {
					return fmt.Errorf("unsafe artwork redirect")
				}
				return nil
			}}
			response, requestErr := client.Get(rawURL)
			if requestErr != nil {
				err = requestErr
			} else {
				defer response.Body.Close()
				if response.StatusCode < 200 || response.StatusCode >= 300 {
					err = fmt.Errorf("artwork response is not an image")
				} else {
					data, readErr := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
					contentType := strings.ToLower(response.Header.Get("Content-Type"))
					if contentType == "" {
						contentType = strings.ToLower(http.DetectContentType(data))
					}
					if readErr != nil || len(data) > 8<<20 {
						err = fmt.Errorf("reading artwork")
					} else if !strings.HasPrefix(contentType, "image/") {
						err = fmt.Errorf("artwork response is not an image")
					} else {
						err = os.WriteFile(path, data, 0600)
					}
				}
			}
		}
	}
	local := ""
	if err == nil {
		local = (&url.URL{Scheme: "file", Path: path}).String()
	}
	s.mu.Lock()
	s.artwork[rawURL], s.artworkBusy[rawURL] = local, false
	latest := s.latest
	s.mu.Unlock()
	if local != "" && latest.Track.ArtURL == rawURL {
		latest.Track.ArtURL = local
		s.Update(latest)
	}
}

// Seeked reports a discontinuous playhead change.
func (s *Service) Seeked(_ time.Duration) {}

func (s *Service) release(fromRunLoop bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed || s.runLoopOwned && !fromRunLoop {
		s.mu.Unlock()
		return
	}
	s.closed = true
	bridge, handle := s.bridge, s.handle
	s.bridge, s.handle = nil, 0
	s.mu.Unlock()
	if fromRunLoop {
		C.chillMediaDestroy(bridge)
	}
	if handle != 0 {
		handle.Delete()
	}
	activeServiceMu.Lock()
	if activeService == s {
		activeService = nil
	}
	activeServiceMu.Unlock()
}

// Close removes the system now-playing session.
func (s *Service) Close() { s.release(false) }
