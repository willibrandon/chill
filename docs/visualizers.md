# REPL visualizer architecture

![Live spectrum visualization in the REPL](../assets/chill.png)

## Audio ownership

The daemon owns playback for the lifetime of a station. `pcmPlayer` implements
the existing `player` interface, so volume, mute, pause, sleep, reconnects and
daemon upgrades retain the same control path. Each player owns:

1. A cancellable yt-dlp resolution for YouTube, including required HTTP headers.
   Direct media URLs and local files go straight to the decoder.
2. One FFmpeg decoder producing interleaved stereo float32 little-endian PCM at
   48 kHz. FFmpeg is an explicit install and doctor requirement.
3. One ten-band parametric equalizer that processes PCM in the decoder pump.
4. One mpv output process reading PCM from stdin, controlled through JSON IPC.
5. A fixed-size PCM ring in `internal/audio`.

The pump equalizes each PCM block once, then sends the same samples to mpv and
the ring. It never performs FFT or terminal/network I/O. Analysis has its own
lock and copies samples under the ring lock before computing. Snapshot results
are cached for 1/30 second across subscribers. A subscriber can be slow without
backing up the audio pipe. FFmpeg input pacing, disabled mpv read-ahead and a
small output buffer limit the visual lead; this is not hardware-clock-exact
presentation synchronization.

Cancellation closes the pipe and terminates both process trees. It also cancels
an in-flight extractor, including its runtime children. The daemon waits for
cleanup before replacing a player. Media EOF or decoder failure flows through
the existing reconnect policy. There is no second stream request for analysis.

## Analysis contract

`audio.Frame` has fixed-size arrays and contains no UI state:

- 2,048-sample Hann-windowed FFT, 64 logarithmic bands from 30 Hz to 20 kHz.
- Stereo power combined **after** FFT, avoiding anti-phase cancellation.
- Independent left/right linear sample peak and RMS.
- Contiguous stereo samples for scope and phase displays.
- Timestamp and sequence for identifying absent/stale audio.

The analysis point is after Chill's equalizer and before mpv volume/mute. UI
labels call this post-EQ audio: changing the curve changes the picture, while a
volume or mute change does not.

## Transport and lifecycle

The daemon protocol is version 4. The `visualize` request upgrades its
connection to newline-delimited JSON snapshots (`version: 1`) at 30 Hz. It is
read-only, uses a dedicated connection and does not start playback. A packet
contains playback state, station generation and an `audio.Frame`.

The server never holds the daemon control mutex while analyzing or writing.
Writes have a 250 ms deadline; disconnected or blocked clients are discarded.
No subscriber means no FFT. Paused/loading/idle states do not request analysis.

The REPL has at most one asynchronous read in flight. Hiding the panel closes
the connection; showing it reconnects. Retry ticks and connection results carry
an ID, so late messages cannot revive a disabled view. Help, tiny windows, off,
and shutdown close the subscription. New generations, pause and stale samples
clear presentation state. Multiple REPLs have independent display settings.

## Rendering and controls

`internal/visualizer` depends only on the analysis contract. `Advance` updates
attack/release, peak hold and bounded history; `Render` is a deterministic,
read-only function of mode and dimensions. Render dimensions and history are
capped. Resizing does not advance animation or resize audio buffers.

`tui_visualizer.go` owns layout, focus, commands and subscription lifecycle.
Compact mode sits below the transcript, preserving transcript mouse coordinates.
Fullscreen hides the prompt and transcript without discarding them. F2 focuses
the visualizer; only that focus captures v/V, arrows, Space and o. Esc restores
the prompt, including any draft. `viz` commands run locally even during an
in-flight diagnostic command.

To add a mode, register a stable name in `visualizer.Modes` and implement its
renderer using the existing frame/history. Do not add playback, subprocess or
socket operations to renderers. To replace the audio backend, implement the
existing player contract and optional `audioFrame() audio.Frame` method.

## Verification

- DSP tests use known-frequency stereo tones, phase inversion and silence.
- Renderer tests cover every mode, tiny/resized canvases, purity and peak decay.
- Subscription/UI tests cover slow readers, pause, stale messages and focus.
- `TestPCMIntegration` runs in the normal `go test ./...` suite and exercises
  real FFmpeg → PCM → mpv with local stereo fixtures and null audio output,
  including start-paused, decoder failure and cleanup. Missing mpv or FFmpeg
  fails the suite with installation instructions; there is no opt-in flag.
- CI installs FFmpeg and exercises playback across macOS, Linux and Windows.
  The separate YouTube workflow verifies the external extractor/network path.
