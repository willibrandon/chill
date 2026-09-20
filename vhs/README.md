# Terminal recordings

Run from the repository root with [VHS](https://github.com/charmbracelet/vhs) installed:

```sh
go build
vhs vhs/readme.tape
```

`readme.tape` writes `assets/chill.png` at 1200 × 720, used by the README. It shows
the station list, a live spectrum visualizer, and command suggestions together.

It drives the real REPL, so it needs mpv, FFmpeg and yt-dlp. It plays chillhop,
waits for live spectrum data, then stops playback after the screenshot. This
changes the current daemon's station, and the commands it types end up in your
REPL history. The pause before `status` gives playback a few seconds of uptime.

The tape selects `./chill` on Linux/macOS, or `./chill.exe` when run from WSL
with a Windows build.
