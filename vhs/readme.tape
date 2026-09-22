# Capture the REPL for the README.
# This plays chillhop, shows its live spectrum, and stops it again.

Require go
Require ffmpeg
Require yt-dlp

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
Type "go run ./vhs/fixtures repl-overview"
Enter
Wait+Screen /chill>/
Show

Type "play chillhop"
Enter
Wait+Screen /playing.*chillhop/
Type "list"
Enter
Wait+Screen /study/
Sleep 6s
Type "status"
Enter
Wait+Screen /chill\[chillhop\]> status/
Sleep 500ms

Type "viz spectrum"
Enter
Wait+Screen /spectrum · 1\/31 · post-EQ audio/
Wait+Screen /[▁▂▃▄▅▆▇█]/
Enter
Wait+Screen /F2 visualizer/

Type "s"
Wait+Screen /commands/
Down 2
Wait+Screen /Enter accepts/
Sleep 2s
Screenshot assets/chill.png
Sleep 1s
Ctrl+q
