# Playback dependencies and diagnostics

Chill includes audio output and decoders for MP3, FLAC, PCM WAV, and Ogg Vorbis.
Local files, supported direct streams, and cached podcasts in these formats play
without additional executables. System audio facilities are still required.
Multichannel sources use FFmpeg for a layout-aware stereo downmix.

| Tool | Enables |
| --- | --- |
| FFmpeg | AAC, ALAC, Opus, WMA, additional containers, HLS, and website audio decoding |
| yt-dlp | YouTube and supported website extraction and playback |
| Deno, Node, or QuickJS | JavaScript challenges used by YouTube extraction |
| ffprobe | Additional metadata and duration probing; included with FFmpeg |

Homebrew and Scoop installations include FFmpeg, yt-dlp, and Deno for full
playback. A downloaded Chill binary can play native formats immediately. It
does not install optional tools automatically. mpv is no longer required.

For Python-based installations, use `pipx install 'yt-dlp[default]'` so the EJS
challenge scripts are included. Update the extractor and its components
together. Deno is the default runtime; Node and QuickJS are also supported.
Runtime compatibility ultimately depends on the installed yt-dlp version.

## Tool settings

Place an optional `tools.json` beside `stations.json` in Chill's configuration
directory. Omitted fields use automatic discovery:

```json
{
  "ffmpeg": "/path/to/ffmpeg",
  "ffprobe": "/path/to/ffprobe",
  "yt_dlp": "/path/to/yt-dlp",
  "js_runtime": "node",
  "js_runtime_path": "/path/to/node"
}
```

`js_runtime` accepts `auto`, `deno`, `node`, or `quickjs`. Auto checks Deno, Node,
then QuickJS. An explicit runtime path requires an explicit runtime selection.
Other executable overrides may be absolute paths or executable names.

Discovery checks explicit overrides, `PATH`, and the directory containing the
Chill executable. Legacy extractor lookup includes `MPV_HOME`, the user mpv
configuration directories, and `portable_config` beside an mpv executable found
on `PATH`. This compatibility lookup does not require mpv for playback.
Set `yt_dlp` explicitly when migrating these locations. General yt-dlp
configuration is ignored. Playback, provider search, and doctor use the same
Chill settings, including the FFmpeg path passed to yt-dlp for segmented streams;
provider browser-cookie settings continue to apply.
Doctor warns when an extractor was found in a legacy portable location.

## Doctor

```sh
chill doctor
chill doctor --audio
chill doctor --stream song.flac
chill doctor --stream lofi-girl --timeout 45s
chill doctor --stations
chill doctor --providers
```

Plain doctor checks local configuration, installed capabilities, output device
enumeration, and daemon compatibility. Missing optional tools produce warnings
and installation guidance, with a successful exit status when core checks pass.
Invalid explicit tool paths or runtime settings fail the check.
It does not start playback, contact streaming services, or change configuration.

`--audio` explicitly opens and closes the selected output without playing sound.
`--stream` prepares the selected source and decodes initial audio without opening
a device. `--stations` checks each station independently. These explicit checks
fail when the requested source or output cannot work. A successful YouTube source
check verifies extraction and decoding with the installed runtime and EJS setup.

An unavailable saved device is retained as a preference. Playback falls back to
the system default and returns when that device is available again. `chill device
list` shows native device IDs and names.

Native finite HTTP sources use validated byte ranges where supported by their
decoder and server. Unsupported ranges, streaming MP3, and chained Vorbis use incremental
decoding to reach a seek position, which can take longer on large remote files.
Live streams keep one metadata-aware connection through native decoding or
FFmpeg fallback. No complete media download is kept in memory.
HTTP reads time out after 15 seconds without incoming data. Time spent paused or
waiting for the output buffer does not count toward this timeout.
