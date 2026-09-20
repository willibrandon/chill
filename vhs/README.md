# Terminal recordings

Run from the repository root with [VHS](https://github.com/charmbracelet/vhs) installed:

```sh
go build
vhs vhs/readme.tape
```

`readme.tape` writes `assets/chill.png` at 1200 × 640, used by the README.

It drives the real REPL, so it needs mpv and yt-dlp, plays chillhop for about twenty seconds, and
stops it again. The commands it types end up in your REPL history. The pause before `status` is
there so the uptime doesn't read `0s`.

On Windows VHS runs bash from WSL, which starts the Windows `chill.exe`. On Linux or macOS change
`./chill.exe` to `./chill` in the tape.
