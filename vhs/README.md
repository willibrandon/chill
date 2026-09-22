# Terminal recordings

Run from the repository root with [VHS](https://github.com/charmbracelet/vhs):

```sh
go run github.com/charmbracelet/vhs@v0.11.0 vhs/readme.tape
go run github.com/charmbracelet/vhs@v0.11.0 vhs/podcasts.tape
go run github.com/charmbracelet/vhs@v0.11.0 vhs/features.tape
go run github.com/charmbracelet/vhs@v0.11.0 vhs/browsers.tape
```

The VHS version is pinned because 0.12.0 cancels FFmpeg before it writes
screenshots. Running through Go keeps the recorder version consistent on every
platform.

`readme.tape` writes `assets/chill.png` at 1200 × 720, used by the README. It
shows the station list, a tall live spectrum visualizer, and command suggestions
together. The capture remains in the mixed REPL layout rather than opening the
fullscreen visualizer.

It drives the real REPL, so it needs FFmpeg and yt-dlp for website playback and recording. It plays chillhop
and waits for live spectrum data. Its temporary interface profile hides the
optional information panels and requests a taller visualizer for this overview.
The pause before `status` gives playback a few seconds of uptime.

The tapes use a Go launcher, so the same sources drive captures on every
supported platform without platform-specific scripts or binary names. The
launcher builds the current tree, removes inherited `NO_COLOR`, uses temporary
configuration and runtime directories, and stops the recording daemon whenever
the REPL exits. Screenshot runs do not persist interface settings or REPL
history.

`podcasts.tape` captures the podcast browser in `assets/podcasts.png`. It opens
the Tech Life feed and plays an episode briefly without changing subscriptions.
It also requires ffprobe, which comes with FFmpeg.

`features.tape` writes `assets/equalizer.png`, `assets/radio.png`, and
`assets/lyrics.png`. It generates quiet local media with deterministic metadata
and embedded lyrics, then removes that temporary media when the REPL exits.

`browsers.tape` writes `assets/library.png`, `assets/providers.png`,
`assets/audio.png`, and `assets/interface.png` for their corresponding guides.
The interface capture previews the Paper palette while the other captures use
Midnight, making the theme-owned selection colors visible. The README keeps only
the overview capture; detailed screens live with the documentation they explain.
