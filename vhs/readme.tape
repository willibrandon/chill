# Capture the REPL for the README.
# Build first: go build
# Uses ./chill on Unix or ./chill.exe under WSL.
# This plays chillhop, shows its live spectrum, and stops it again.

Require mpv
Require ffmpeg
Require yt-dlp

Set Shell bash
Set FontSize 14
Set Width 1200
Set Height 720
Set Padding 20
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 0ms
Set Framerate 30
Set WaitTimeout 60s

Hide
Type "if [ -x ./chill ]; then ./chill -i; else ./chill.exe -i; fi"
Enter
Wait+Screen /chill>/
Show

Type "play chillhop"
Enter
Wait+Screen /playing │ chillhop/
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
Wait+Screen /spectrum · 1\/31 · source audio/
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

Ctrl+c
Type "stop"
Enter
Wait+Screen /stay chill/
Ctrl+q
