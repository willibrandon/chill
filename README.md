# chill

Terminal lofi radio. Streams the best 24/7 lofi beats from YouTube.

```
        ╭──────────────────╮
        │   ░▒▓ chill ▓▒░  │
        ╰──────────────────╯
```

## Install

Requires [mpv](https://mpv.io/) and [yt-dlp](https://github.com/yt-dlp/yt-dlp).

**macOS:**
```bash
brew install mpv yt-dlp
```

**Linux:**
```bash
sudo apt install mpv
pip install yt-dlp
```

**Windows:**
```powershell
choco install mpv yt-dlp
```

Then install chill:
```bash
go install github.com/willibrandon/chill@latest
```

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
chill --status       # show what's playing
chill --stop         # stop playback
chill --list         # show all stations
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

## Interactive Mode

`chill -i` launches a REPL with tab-completion:

```
        ╭──────────────────╮
        │   ░▒▓ chill ▓▒░  │
        ╰──────────────────╯
  type 'list' for stations, 'quit' to exit

♪ play ch<TAB>
      chillhop   Chillhop Radio - jazzy & lofi hip hop
      chillout   Chillout Lounge - calm & relaxing
```

Commands: `play`, `skip`, `pause`, `resume`, `toggle`, `status`, `list`, `stop`, `quit`

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
