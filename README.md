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
go install github.com/yourusername/chill@latest

# or build from source
git clone https://github.com/yourusername/chill
cd chill
go install .
```

Make sure `~/go/bin` is in your `PATH`:

```bash
export PATH="$HOME/go/bin:$PATH"
```

## Usage

```bash
chill                      # play default (lofi-girl)
chill chillhop             # play a specific station
chill --station=chillout   # alternate syntax
chill --list               # show all stations
```

## Stations

| Station | Description |
|---------|-------------|
| `lofi-girl` | Lofi Girl - beats to relax/study to |
| `chillhop` | Chillhop Radio - jazzy & lofi hip hop |
| `chillout` | Chillout Lounge - calm & relaxing |
| `code-radio` | Code Radio - beats to study & code to |
| `sleep` | Lofi - beats to sleep/relax to |
| `study` | Lofi - beats to study/relax to |

## Controls

| Key | Action |
|-----|--------|
| `q` | quit |
| `m` | mute |
| `9` / `0` | volume down / up |
| `←` / `→` | seek |

## License

MIT
