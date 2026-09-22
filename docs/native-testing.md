# Native playback validation

Build with Go and a C compiler, with CGO enabled. Release builds contain the
native backend; consumers do not need the compiler or development headers.
Linux release binaries target glibc 2.31 or newer and use installed PulseAudio
or ALSA runtime libraries. Windows releases use the UCRT available on Windows 10
and later. Releases retain AMD64 and ARM64 assets for Linux, macOS, and Windows.

```sh
go build .
go vet ./...
go test ./...
go test -race ./...
go test ./internal/playback -run '^$' -bench BenchmarkOutputCallback -benchmem
python3 -m unittest discover -s .github/scripts -p 'test_*.py'
```

Native codec tests use committed generated fixtures and explicitly hide helper
executables. Additional AAC podcast integration tests run when FFmpeg is
installed. Normal CI installs it so this coverage cannot be accidentally skipped.
The separate YouTube workflow checks actual external-service behavior.

Regression coverage checks malformed decoder inputs, cubic volume gain,
downsampling alias rejection, MP3 delay/padding and reference alignment,
chained Vorbis audio and tags, queue changes during output drain, HTTP idle
timeouts, portable extractor discovery, and configured FFmpeg propagation.
For an extended malformed-input check, run:

```sh
go test ./internal/playback -run '^$' -fuzz FuzzNativeDecoders -fuzztime 30s
```

## CLI integration

```sh
go build -tags chill_test_audio -o /tmp/chill-native .
python3 .github/scripts/playback.py --binary /tmp/chill-native
```

The harness enables a paced null output through `CHILL_TEST_AUDIO=null`. This
backend is compiled only with `chill_test_audio`; release builds ignore that
environment variable. The ordinary unit suite injects its own test devices.
The harness covers playback, pause, volume, mute, EQ, sleep, podcasts, seeking,
speed, queues, and process cleanup. `--legacy-bin-dir` adds old-to-new daemon
upgrade coverage; only that test job installs the historical output dependency.

## Native output and release checks

CI opens an actual Linux native backend through a virtual PulseAudio sink.
Before a stable release, run these checks on macOS and Windows hardware too:

1. Run `chill doctor --audio` and play each native fixture with helper tools absent.
2. Exercise device selection, pause/resume, and each audio profile in daemon and
   foreground modes; confirm device changes do not change system routing.
3. Disconnect and reconnect a selected device; confirm fallback and return.
4. Check exclusive mode on a supporting device and rejection on an unsupported one.
5. Listen to speech and music at 0.5×, 1×, 1.5×, and 3×, including changes mid-track.
6. Check gapless local transitions, seeking, EOF tails, and visualizer timing.
7. Upgrade an active daemon and verify queue, position, pause, speed, volume, EQ,
   device preferences, and the sleep deadline.

CI builds and tests on AMD64 and ARM64 runners for all three operating systems.
Release jobs build native audio for all six targets, report dynamic-library
imports, and execute each archive on a matching runner before publishing.
Archive names and checksums remain compatible with existing updates, and package
updates preserve unrelated Homebrew/Scoop dependencies.
