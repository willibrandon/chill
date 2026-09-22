# Browses a public feed, plays briefly, and stops the isolated session.
Require go
Require ffmpeg
Require ffprobe
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
Type "podcasts https://podcasts.files.bbci.co.uk/p01plr2p.rss"
Enter
Wait+Screen /300 items/
Enter
Wait+Screen /playing │/
Sleep 2s
Screenshot assets/podcasts.png
Sleep 1s
Ctrl+q
