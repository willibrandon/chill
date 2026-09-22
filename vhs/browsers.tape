# Capture the F7 library, F8 providers, F9 audio, and F10 interface screens.

Require go
Require mpv
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
Type "go run ./vhs/fixtures repl"
Enter
Wait+Screen /chill>/
Show

# VHS 0.11.0 has no named F-key command, so send their xterm sequences.
Escape
Type "[18~"
Wait+Screen /Library  \/  Library/
Sleep 1s
Screenshot assets/library.png
Sleep 500ms

Escape
Type "[19~"
Wait+Screen /Providers  \/  Providers/
Sleep 1s
Screenshot assets/providers.png
Sleep 500ms

Escape
Type "[20~"
Wait+Screen /Audio Output/
Sleep 1s
Screenshot assets/audio.png
Sleep 500ms

Escape
Type "[21~"
Wait+Screen /interface  live preview/
Sleep 1s
Screenshot assets/interface.png
Sleep 500ms

Ctrl+q
