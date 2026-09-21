# Terminal recordings

Run from the repository root with [VHS](https://github.com/charmbracelet/vhs):

```sh
go run github.com/charmbracelet/vhs@v0.11.0 vhs/readme.tape
go run github.com/charmbracelet/vhs@v0.11.0 vhs/podcasts.tape
go run github.com/charmbracelet/vhs@v0.11.0 vhs/features.tape
```

The VHS version is pinned because 0.12.0 cancels FFmpeg before it writes
screenshots. Running through Go keeps the recorder version consistent on every
platform.

`readme.tape` writes `assets/chill.png` at 1200 × 720, used by the README. It shows
the station list, a live spectrum visualizer, and command suggestions together.

It drives the real REPL, so it needs mpv, FFmpeg and yt-dlp. It plays chillhop,
waits for live spectrum data, then stops playback after the screenshot. This
changes the current daemon's station, and the commands it types end up in your
REPL history. The pause before `status` gives playback a few seconds of uptime.

The tapes use `go run`, so the same sources drive captures on every supported
platform without platform-specific scripts or binary names.

`podcasts.tape` captures the podcast browser in `assets/podcasts.png`. It opens
the Tech Life feed, subscribes, plays an episode briefly, then stops. It also
requires ffprobe, which comes with FFmpeg.

`features.tape` writes `assets/equalizer.png`, `assets/radio.png`, and
`assets/lyrics.png`. It uses a quiet local stream with deterministic metadata;
the server fixture is removed when the tape finishes. The lyrics lookup uses
the same public service as Chill.
