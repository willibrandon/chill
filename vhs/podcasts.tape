# Build with go build, then run from the repository root.
# Browses a public feed, subscribes, plays briefly, and stops playback.
Require mpv
Require ffmpeg
Require ffprobe
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
Type "podcasts https://podcasts.files.bbci.co.uk/p01plr2p.rss"
Enter
Wait+Screen /300 items/
Type "f"
Wait+Screen /Subscribed: Tech Life/
Enter
Wait+Screen /playing.*Tech Life/
Sleep 2s
Screenshot assets/podcasts.png
Sleep 1s
Hide
Escape
Sleep 300ms
Escape
Wait+Screen /chill\[podcast\]>/
Type "stop"
Enter
Wait+Screen /stay chill/
Ctrl+q
