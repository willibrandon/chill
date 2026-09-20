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

Make sure your Go bin directory is in your `PATH`:
- macOS/Linux: `export PATH="$HOME/go/bin:$PATH"`
- Windows: Add `%USERPROFILE%\go\bin` to your PATH

## Usage

```bash
chill                # play lofi-girl (starts daemon automatically)
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
chill --version      # show version
chill --fg           # run in foreground (no daemon)
```

## Architecture

chill uses a client-server architecture. The first `chill` command spawns a background daemon that manages playback. Subsequent commands communicate with the daemon over IPC (Unix socket on macOS/Linux, TCP on Windows).

```
┌─────────────┐      ┌─────────────┐      ┌─────────────┐
│ chill       │ ──── │ daemon      │ ──── │ mpv         │
│ (client)    │ IPC  │ (server)    │      │ (playback)  │
└─────────────┘      └─────────────┘      └─────────────┘
```

This means:
- Music keeps playing after the command exits
- Control playback from any terminal
- Fast command execution (no startup delay)

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

Add any YouTube stream with `chill add <name> <url> [description]`, or edit
`~/.config/chill/stations.json` (`%AppData%\chill\stations.json` on Windows):

```json
{
  "stations": [
    {"name": "synthwave", "url": "https://www.youtube.com/watch?v=4xDzrJKXOOY", "desc": "Synthwave Radio"}
  ]
}
```

A station with the name of a built-in overrides it. The running daemon picks
up edits with `chill -i` then `reload`, or it loads the file when it starts.

## Interactive Mode

`chill -i` opens a fullscreen REPL that suggests commands and stations as you type, shown above.

Commands: `play`, `vol`, `mute`, `skip`, `pause`, `resume`, `toggle`, `status`, `list`, `add`, `reload`, `stop`, `clear`, `help`, `quit`. A station name on its own plays that station.

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

## License

MIT
