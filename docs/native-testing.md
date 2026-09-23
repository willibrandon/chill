# Playback validation

`go build -o ./chill .` and `go vet ./...` check compilation. They do not check sound.
The source tests use actual decoders and generated media files. No substitute
player or silent output backend is installed by the test suite.

The default suite is silent and does not start players, access live stations,
or build another executable:

```sh
go test ./...
go test -race ./...
```

Audible integration checks are separate. Run them explicitly on macOS 14.2 or
newer with a physical audio output, FFmpeg, yt-dlp, and clang:

```sh
go test -tags audio_integration -run '^TestMacOSStationPlayback$' .
# Or check an already-built binary without building it again:
python3 scripts/check_macos_playback.py --binary ./chill --report /tmp/chill-audio.json
```

These commands play test tones and music. The default tests never invoke them.
The integration harness uses two-second measurements and stops on the first
failure, including a race detector report.

The macOS integration tests use a separate CoreAudio process tap to measure the application's
output. They exercise an HTTP audio file, pause/resume, mute/unmute, sample-rate
changes, and the built-in Chillhop station. Each audible window must contain at
least 90% nonzero samples and a measurable signal, and the playback clock must
advance. Paused and muted output must be silent. Missing output or unavailable
measurement is a failure. No microphone recording is made or retained.

The script uses a temporary `CHILL_CONFIG_DIR` and runtime directory, starts the
actual built daemon, and stops it afterward. It does not edit the listening
profile used by your normal session. `--local-only` omits the external station;
it is useful for separating a network failure from a device failure.

`chill status --json` includes output sample counts, queued frames, missing frames,
last output pull age, and peak level. These measure the application's delivery to
the driver. They are diagnostic evidence, not a substitute for the independent
CoreAudio measurement or an acoustic listening check.

Hosted CI runs the default source tests and build checks. Physical output tests
require the `audio_integration` build tag; a green hosted job does not establish
working sound.
The separate **macOS playback** workflow requires a self-hosted macOS runner with
an `audio` label and a physical output. No virtual or silent device is used.

The playback comparison reference is the read-only CLIAMP checkout at commit
`57ae3badf75f1f370178df34c6382c069e6564aa`. Chill's default macOS output follows
its Beep/Oto AudioQueue approach, persistent process context, native device sample
rate selection, and asynchronous output error checking. The adapted rate query's
MIT notice is in `internal/playback/LICENSE.cliamp`.
