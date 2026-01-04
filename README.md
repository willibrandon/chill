# chill

Terminal lofi radio. Streams the best 24/7 lofi beats from YouTube.

```
        ╭──────────────────╮
        │   ░▒▓ chill ▓▒░  │
        ╰──────────────────╯
```

## Install

```bash
# requires mpv and yt-dlp
brew install mpv yt-dlp

# install chill
go install github.com/willibrandon/chill@latest

# or build from source
git clone https://github.com/willibrandon/chill
cd chill
go install .
```

Make sure `~/go/bin` is in your `PATH`:

```bash
export PATH="$HOME/go/bin:$PATH"
```

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

chill uses a client-server architecture. The first `chill` command spawns a background daemon that manages playback. Subsequent commands communicate with the daemon over a Unix socket.

```
┌─────────────┐      ┌─────────────┐      ┌─────────────┐
│ chill       │ ──── │ daemon      │ ──── │ mpv         │
│ (client)    │ unix │ (server)    │      │ (playback)  │
└─────────────┘ sock └─────────────┘      └─────────────┘
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

Use `--fg` for the original interactive experience with mpv controls:

| Key | Action |
|-----|--------|
| `q` | quit |
| `m` | mute |
| `9` / `0` | volume down / up |
| `←` / `→` | seek |

## License

MIT
