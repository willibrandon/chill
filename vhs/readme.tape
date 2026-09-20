# Capture the REPL for the README.
# Build first: go build
# On Windows VHS runs bash from WSL, which starts the Windows binary. Elsewhere
# change ./chill.exe to ./chill.
# This plays chillhop for a few seconds and stops it again.

Require mpv
Require yt-dlp

Set Shell bash
Set FontSize 14
Set Width 1200
Set Height 640
Set Padding 20
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 0ms
Set Framerate 1
Set WaitTimeout 60s

Hide
Type "./chill.exe -i"
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
Wait+Screen /▶/

Type "s"
Wait+Screen /commands/
Down 2
Wait+Screen /Enter accepts/
Sleep 2s
Screenshot assets/chill.png
Sleep 4s

Ctrl+c
Type "stop"
Enter
Wait+Screen /stay chill/
Ctrl+q
