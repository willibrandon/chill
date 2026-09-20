# chill

Terminal lofi radio. 24/7 streams from YouTube.

![chill playing chillhop with a live spectrum visualizer and command suggestions in the REPL](https://raw.githubusercontent.com/willibrandon/chill/main/assets/chill.png)

## Install

### Homebrew (macOS and Linux)

```bash
brew install willibrandon/tap/chill
```

### Scoop (Windows)

```powershell
scoop bucket add willibrandon https://github.com/willibrandon/scoop-bucket
scoop bucket add extras
scoop install chill
```

Both packages include mpv, FFmpeg, yt-dlp, and Deno.

### Go or release binary

Install [mpv](https://mpv.io/), [FFmpeg](https://ffmpeg.org/), [yt-dlp](https://github.com/yt-dlp/yt-dlp),
and [Deno](https://deno.com/) first.

macOS:
```bash
brew install mpv ffmpeg yt-dlp deno
```

Linux:
```bash
sudo apt install mpv ffmpeg pipx
pipx install yt-dlp
```

Also install [Deno](https://docs.deno.com/runtime/getting_started/installation/).

Windows:
```powershell
choco install mpv ffmpeg yt-dlp deno
```

Then install chill:
```bash
go install github.com/willibrandon/chill@latest
```

Or download a [release binary](https://github.com/willibrandon/chill/releases)
and put it in your `PATH`.

For Go installs, add the Go bin directory to your `PATH`:
- macOS/Linux: `export PATH="$HOME/go/bin:$PATH"`
- Windows: Add `%USERPROFILE%\go\bin` to your PATH

### Update

Use `chill update` for Go installs and release binaries,
`brew upgrade willibrandon/tap/chill` for Homebrew, or `scoop update chill` for Scoop.

## Usage

```bash
chill                   # play default station (starts daemon automatically)
chill chillhop          # play specific station
chill -i                # interactive mode (repl)
chill --skip            # skip to random station
chill --toggle          # pause/resume
chill --vol 60          # set volume (also +5, -10, up, down)
chill --mute            # toggle mute
chill --status          # show what's playing
chill --status --json   # show status as JSON
chill doctor            # check mpv, FFmpeg, yt-dlp, runtime, and daemon versions
chill doctor --stations # check station streams
chill doctor --stream n # check one station
chill doctor --logs     # show startup logs
chill --stop            # stop playback
chill --list            # show all stations
chill add n url desc    # save your own station
chill remove n          # remove a custom station or restore a built-in
chill default n         # choose the station played by chill
chill --sleep 45m       # stop playback after 45 minutes
chill --sleep off       # cancel the sleep timer
chill --version         # show version
chill --help            # show help
chill update            # install the latest release
chill --fg              # run in foreground (no daemon)
```

## Architecture

chill runs mpv in the background, so music keeps playing when you close the
terminal. You can control it from another terminal.

```text
yt-dlp resolves → FFmpeg decodes → daemon PCM pipe → mpv audio output
                                       │
                                  bounded audio tap
                                       │
                                  FFT / stereo levels
                                       │
CLI/REPL ←── control IPC ──→ daemon ── snapshots ──→ REPL visualizer
```

After an update, the next control command restarts an older daemon and restores
your playback settings. Volume is saved between sessions.

The daemon decodes one stream into 48 kHz stereo PCM. Visualizers analyze the
same samples sent to playback; they do not open another network stream or capture
system audio. FFT work runs only while a REPL is subscribed. `doctor` checks the
new FFmpeg dependency as well as mpv, yt-dlp, and the YouTube JavaScript runtime.

If a stream disconnects, chill keeps reconnecting with increasing delays, capped
at 30 seconds. `--status` and the REPL show the retry count and countdown; JSON
status includes `state: "reconnecting"` and `retry_at` while waiting. Volume,
mute, pause, and the sleep deadline survive reconnects. Stop playback or switch
stations to cancel a reconnect.

### Sleep timer

`chill --sleep 45m` stops playback after 45 minutes. Durations such as `1h30m`
work too. The timer keeps running while paused or switching stations.
Use `chill --sleep off` to cancel it.

## Stations

| Station | Description |
|---------|-------------|
| `lofi-girl` | Lofi Girl - beats to relax/study to |
| `chillhop` | Chillhop Radio - jazzy & lofi hip hop |
| `chillout` | Chillout Lounge - calm & relaxing |
| `code-radio` | Code Radio - beats to study & code to |
| `sleep` | Lofi - beats to sleep/relax to |
| `study` | Lofi - beats to study/relax to |

The default is `lofi-girl`. Change it with `chill default <name>`.

### Your own stations

Add a YouTube stream with `chill add <name> <url> [description]`, or edit the config:

- macOS: `~/Library/Application Support/chill/stations.json`
- Linux: `~/.config/chill/stations.json` (or `$XDG_CONFIG_HOME/chill/stations.json`)
- Windows: `%AppData%\chill\stations.json`

```json
{
  "default_station": "synthwave",
  "stations": [
    {
      "name": "synthwave",
      "url": "https://www.youtube.com/watch?v=4xDzrJKXOOY",
      "desc": "Synthwave Radio - retro electronic beats"
    }
  ]
}
```

A custom station with a built-in name overrides it. `chill remove <name>` removes
the custom station and restores any built-in. After editing the file by hand,
run `reload` in the REPL.

## Interactive Mode

`chill -i` opens the REPL. Type `help` for commands or press F1 for keys.
A station name starts playback. `doctor` takes the same options as the CLI.
Diagnostic findings appear as each check finishes. Press `Ctrl+C` (when nothing
is selected) or type `cancel` to cancel diagnostics and discard queued commands.
Quitting also cancels diagnostics; music keeps playing.

`sleep` plays the station. `sleep 45m` sets the timer.

| Key | |
|-----|---|
| `Tab` | complete with the highlighted suggestion |
| `→` | take the ghost text |
| `↑` / `↓` | pick a suggestion or browse history |
| `Enter` | run the line, or take a suggestion picked with `↑` / `↓` |
| `Esc` | dismiss the suggestions |
| `PgUp` / `PgDn` | scroll the transcript |
| `Shift+↑` / `Shift+↓` | select lines of the transcript |
| `y` / `Enter` / `Ctrl+C` | copy what is selected |
| mouse | drag to select, right click to copy, or to paste when nothing is selected |
| `Ctrl+C` | cancel diagnostics, otherwise clear the line (when nothing is selected) |
| `Ctrl+L` | clear the screen |
| `F1` | help |
| `F2` | open/focus the visualizer; return to the prompt when focused |
| `Ctrl+Q` | quit, music keeps playing |

### Visualizers

31 modes, including spectrum bars, waveforms, matrix, flame, and stereo meters.
Press **F2** or type `viz` to open. While focused, **v** cycles modes,
**V** toggles fullscreen, and **Enter** returns to the prompt.

Use `viz led` to pick a mode, `viz list` to see them all, or `viz off` to close.

## Foreground Mode

Use `--fg` to run in the terminal with mpv controls:

| Key | Action |
|-----|--------|
| `q` | quit |
| `m` | mute |
| `9` / `0` | volume down / up |
| `←` / `→` | seek |

## Build and test

Install mpv and FFmpeg first. The normal test suite includes real playback
integration tests using generated local audio and null output—no audio device,
network access, or test opt-in environment variables are needed. Missing test
dependencies fail with installation instructions.

```bash
go build .
go test ./...
```

## License

MIT
