# Capture the F4 equalizer, F5 radio browser, and F6 lyrics view.
# The fixture supplies a quiet local ICY stream with known metadata.

Require mpv
Require ffmpeg
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
Env NO_COLOR ""
Env CLICOLOR_FORCE "1"
Env COLORTERM "truecolor"

Hide
Type "go run ./vhs/fixtures start"
Enter
Wait+Screen /fixture ready/
Type "go run . -i"
Enter
Wait+Screen /chill>/
Show

Type "radio play http://127.0.0.1:18765/stream Showcase"
Enter
Wait+Screen /Traditional - Auld Lang Syne/
Wait+Screen /playing.*Showcase/
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
Wait+Screen /chill\[Showcase\]>/
Type "radio"
Enter
Wait+Screen /Radio.*Radio/
Sleep 1s
Screenshot assets/radio.png
Sleep 500ms

Escape
Wait+Screen /chill\[Showcase\]>/
Type "lyrics"
Enter
Wait+Screen /Lyrics.*Auld Lang Syne/
Wait+Screen /live streams use manual scrolling/
Sleep 1s
Screenshot assets/lyrics.png
Sleep 500ms

Escape
Wait+Screen /chill\[Showcase\]>/
Type "stop"
Enter
Wait+Screen /stay chill/
Ctrl+q
Hide
Type "go run ./vhs/fixtures stop"
Enter
Wait+Screen /fixture stopped/
