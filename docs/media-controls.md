# System media controls

The process that owns playback also owns one operating-system media session.
Daemon playback keeps that session alive after the REPL exits. `--fg` owns a
session for the lifetime of the foreground screen. On Linux, a foreground
player automatically takes a distinct MPRIS name if the daemon already owns the
stable one.

Published state includes play, pause and stopped status; station, local track,
podcast and artist text; source URL; artwork; volume; finite-media duration and
position; and whether next, previous, or seeking are currently meaningful.
Updates occur on state and metadata changes and once per second while playing.

## Controls

| System action | Radio | Finite media |
| --- | --- | --- |
| Play / Pause / Toggle | Resume or pause | Resume or pause |
| Stop | Stop audio; daemon remains available | Stop audio; daemon remains available |
| Next | Play the next universal queue item, or choose another station | Play the next universal queue item |
| Previous | Return to the previous station | Restart or return to the previous item |
| Seek | Not advertised | Relative or absolute seek |
| Volume | Persist a 0–100 level | Persist a 0–100 level |

The previous and forward station stacks are bounded by the playback session.
They reset when playback stops or switches to finite media. Queue behavior is
the same as the `next`, `prev`, and `seek` commands.

## Platforms

Linux exports `org.mpris.MediaPlayer2.chill` on the user's D-Bus session, so
desktop shells and tools such as `playerctl` see the same state. The MPRIS
track identifier changes with metadata, stale absolute seek requests are
ignored, and seek discontinuities emit the standard signal.

macOS uses MediaPlayer and AppKit. The required application run loop stays on
the main operating-system thread while playback and the terminal interface run
normally on Go goroutines. Lock-screen and Control Center commands are routed
back to the active player.

Windows creates a hidden owner window and registers a System Media Transport
Controls session through the Windows Runtime. It publishes display metadata,
remote artwork, playback status and podcast timeline data, and handles button
and absolute-position events. Both Intel and ARM release binaries use this
path.
