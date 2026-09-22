# Capture the F4 equalizer, F5 radio browser, and F6 lyrics view.
# The fixture supplies quiet local media with deterministic metadata and lyrics.

Require ffmpeg
Require ffprobe
Require go

Set FontSize 14
Set Width 1200
Set Height 720
Set Padding 20
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 0ms
Set Framerate 30
Set WaitTimeout 60s
Env CLICOLOR_FORCE "1"
Env COLORTERM "truecolor"

Hide
Type "go run ./vhs/fixtures repl-media"
Enter
Wait+Screen /chill>/
Show

Type "play vhs/fixtures/showcase.m4a"
Enter
Wait+Screen /playing.*Auld Lang Syne/
Type "eq"
Enter
Wait+Screen /equalizer.*preset/
Type "E"
Wait+Screen /equalizer.*preset/
Sleep 1s
Screenshot assets/equalizer.png
Sleep 500ms

Type "e"
Escape
Wait+Screen /chill\[track\]>/
Type "radio"
Enter
Wait+Screen /Radio.*Radio/
Sleep 1s
Screenshot assets/radio.png
Sleep 500ms

Escape
Wait+Screen /chill\[track\]>/
Type "lyrics"
Enter
Wait+Screen /Lyrics.*Auld Lang Syne/
Wait+Screen /Should auld acquaintance/
Sleep 1s
Screenshot assets/lyrics.png
Sleep 500ms

Ctrl+q
