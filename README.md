# chill

Terminal lofi radio. Streams the best 24/7 lofi beats from YouTube.

![chill playing chillhop, with suggestions open in the REPL](https://raw.githubusercontent.com/willibrandon/chill/main/assets/chill.png)

## Install

Requires [mpv](https://mpv.io/) and [yt-dlp](https://github.com/yt-dlp/yt-dlp).

**macOS:**
```bash
brew install mpv yt-dlp
```

**Linux:**
```bash
sudo apt install mpv pipx
pipx install yt-dlp
```

**Windows:**
```powershell
choco install mpv yt-dlp
```

Then install chill:
```bash
go install github.com/willibrandon/chill@latest
```

Or grab a binary for your platform from the [releases page](https://github.com/willibrandon/chill/releases) and put it somewhere in your `PATH`.

To update an installed copy:

```bash
chill update
```

`chill -update`, `chill --update`, and `chill upgrade` work too. This downloads
the latest release for your platform, verifies its checksum, and updates the
running daemon while preserving playback settings.

Make sure your Go bin directory is in your `PATH`:
- macOS/Linux: `export PATH="$HOME/go/bin:$PATH"`
- Windows: Add `%USERPROFILE%\go\bin` to your PATH

## Usage

```bash
chill                # play default station (starts daemon automatically)
chill chillhop       # play specific station
chill -i             # interactive mode (repl)
chill --skip         # skip to random station
chill --toggle       # pause/resume
chill --vol 60       # set volume (also +5, -10, up, down)
chill --mute         # toggle mute
chill --status       # show what's playing
chill --stop         # stop playback
chill --list         # show all stations
chill add n url desc # save your own station
chill remove n       # remove a custom station or restore a built-in
chill default n      # choose the station played by chill
chill --sleep 45m    # stop playback after 45 minutes
chill --sleep off    # cancel the sleep timer
chill --version      # show version
chill update         # install the latest release
chill --fg           # run in foreground (no daemon)
```

## Architecture

chill uses a client-server architecture. The first playback command spawns a
background daemon. CLI commands and the REPL communicate with that daemon over
IPC: a Unix socket on macOS/Linux, or localhost TCP on Windows.

The daemon starts and supervises mpv, sends playback controls over mpv's JSON
IPC, and listens for player events to track loading and failures. This second
connection uses a Unix socket on macOS/Linux and a named pipe on Windows. mpv
uses yt-dlp to resolve YouTube streams.

```
┌─────────────┐      ┌─────────────┐           ┌─────────────┐
│ chill       │ ◀──▶ │ daemon      │ ◀───────▶ │ mpv         │
│ (CLI/REPL)  │ IPC  │ (server)    │ JSON IPC  │ (playback)  │
└─────────────┘      └─────────────┘           └─────────────┘
```

This means:
- Music keeps playing after the command exits
- Control playback from any terminal
- Playback controls use the running player; starting a stream still takes time
- The daemon owns reconnects and sleep timers, so they work after the client exits

After installing a newer version, your next command automatically upgrades an
outdated daemon. Playback restarts once, preserving your station, volume, mute,
pause state, and sleep timer.

Volume, mute, and pause/resume use mpv's native IPC controls, so adjusting the
volume never restarts the stream or resumes paused music. Mute preserves your
volume, and the last volume is remembered across daemon restarts in `state.json`
beside the station config. Mute itself is temporary.

Status distinguishes `loading`, `playing`, `paused`, `failed`, and `idle`.
If a stream drops or fails to load, chill reports the player error and retries
up to three times, after 1, 2, and 4 seconds. Each load has a 45-second timeout.
A minute of successful playback replenishes the retry budget. Errors and retry
progress also appear in the REPL; use `play` or choose another station to try
again after the retries are exhausted.

### Sleep timer

While a station is playing or loading, `chill --sleep 45m` schedules playback to
stop. Durations accept units such as `30s`, `45m`, or `1h30m`. A new timer replaces
the previous one; `chill --sleep off` cancels it. `chill --status` and the REPL
status bar show the remaining time.

The timer continues while paused, switching stations, or reconnecting. When it
expires, playback stops and the daemon becomes idle; `chill --toggle` starts the
default station again. Explicitly stopping chill clears the timer. Timers are
not restored across daemon restarts.

## Stations

| Station | Description |
|---------|-------------|
| `lofi-girl` | Lofi Girl - beats to relax/study to |
| `chillhop` | Chillhop Radio - jazzy & lofi hip hop |
| `chillout` | Chillout Lounge - calm & relaxing |
| `code-radio` | Code Radio - beats to study & code to |
| `sleep` | Lofi - beats to sleep/relax to |
| `study` | Lofi - beats to study/relax to |

### Your own stations

Add any YouTube stream with `chill add <name> <url> [description]`, or edit the
station config:

- **macOS:** `~/Library/Application Support/chill/stations.json`
- **Linux:** `~/.config/chill/stations.json` (or `$XDG_CONFIG_HOME/chill/stations.json`)
- **Windows:** `%AppData%\chill\stations.json`

```json
{
  "default_station": "synthwave",
  "stations": [
    {
      "name": "synthwave",
      "url": "https://www.youtube.com/watch?v=4xDzrJKXOOY",
      "desc": "Synthwave Radio - retro electronic beats"
    },
    {
      "name": "jazz",
      "url": "https://www.youtube.com/watch?v=HuFYqnbVbzY",
      "desc": "Jazz Cafe - smooth jazz for work"
    },
    {
      "name": "rain",
      "url": "https://www.youtube.com/watch?v=mPZkdNFkNps",
      "desc": "Rain on a window - for deep focus"
    }
  ]
}
```

A station with the name of a built-in overrides it. `chill remove <name>` removes
a custom station; removing an override restores the built-in. Built-ins cannot
be removed. `chill default <name>` chooses what `chill`, REPL `play`, and
`chill --fg` play without a station argument. The initial default is `lofi-girl`;
removing a custom station that was the default returns to `lofi-girl`.

Add, remove, and default commands update the running daemon automatically. For
manual file edits, use `chill -i` then `reload`, or restart the daemon. Reload
rebuilds the list from the built-ins and the current file, including removals.
Reloading or removing a station does not interrupt a stream already playing.

## Interactive Mode

`chill -i` opens a fullscreen REPL that suggests commands and stations as you type, shown above.

Commands: `play`, `vol`, `mute`, `skip`, `pause`, `resume`, `toggle`, `status`, `list`, `add`, `remove`, `default`, `sleep`, `reload`, `stop`, `clear`, `help`, `quit`. A station name on its own plays that station.

Use `sleep 45m` or `sleep off` for the timer. `sleep` on its own still plays the
sleep station, as does `play sleep`.

| Key | |
|-----|---|
| `Tab` | complete with the highlighted suggestion |
| `→` | take the ghost text |
| `↑` / `↓` | pick a suggestion, otherwise walk through history (kept across sessions) |
| `Enter` | run the line, or take a suggestion picked with `↑` / `↓` |
| `Esc` | dismiss the suggestions |
| `PgUp` / `PgDn` | scroll the transcript, so does the mouse wheel |
| `Shift+↑` / `Shift+↓` | select lines of the transcript |
| `y` / `Enter` / `Ctrl+C` | copy what is selected |
| mouse | drag to select, right click to copy, or to paste when nothing is selected |
| `Ctrl+C` | clear the line, when nothing is selected |
| `Ctrl+L` | clear the screen |
| `F1` | help |
| `Ctrl+Q` | quit, music keeps playing |

## Foreground Mode

Use `--fg` to run in the terminal with mpv controls:

| Key | Action |
|-----|--------|
| `q` | quit |
| `m` | mute |
| `9` / `0` | volume down / up |
| `←` / `→` | seek |

## Build and test

```bash
go build .
go test ./...
```

With mpv installed, run the playback integration test:

```bash
CHILL_TEST_MPV=1 go test -count=1 -run TestMPVIntegration -v ./...
```

## License

MIT
