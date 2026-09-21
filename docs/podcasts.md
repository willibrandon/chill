# Podcasts

![Browsing podcast episodes](../assets/podcasts.png)

Run `chill podcasts`, type `podcasts` in the REPL, or press F3. The browser has
Top Shows, Categories, Subscriptions, Inbox, Downloads, Download Settings, and
Search. The top chart defaults to the US; `chill podcasts country gb` saves a
different two-letter country code.
Categories use Apple's genre-filtered search. No account or API key is needed.

## Browser keys

| Key | Action |
| --- | --- |
| Up / Down, PgUp / PgDn | Browse the list |
| Enter | Open a show or play an episode |
| f | Subscribe or unsubscribe from the selected show |
| / | Filter the list; search from the home screen |
| Ctrl+F | Search shows or paste an RSS URL |
| Ctrl+R | Refresh the current list or feed |
| q | Queue the selected episode |
| l | Queue the newest episode of the selected show, or one per inbox show |
| v | Cycle the inbox through unplayed, all, and played episodes |
| d | Download the selected episode |
| D | Remove the selected offline download |
| R | Retry the selected offline download |
| P | Pin or unpin a download against automatic cleanup |
| Left / Right | Adjust the selected Download Settings value |
| r | Restart the selected episode |
| Shift+Left / Shift+Right | Skip back / forward 30 seconds |
| Space | Pause or resume |
| [ / ] | Decrease / increase speed by 0.25x |
| Esc | Go back; close the browser from its home screen |
| F3 | Return directly to the REPL prompt |
| Ctrl+C | Cancel a pending request |
| Ctrl+Q | Quit the REPL; playback continues |

Letter shortcuts apply inside the browser, not while typing a search or command.
The browser preserves your prompt draft. Returning to the prompt lets you use
the visualizer, station commands, volume, mute, and sleep timer as usual.

## Command line

```sh
chill podcasts
chill podcasts https://example.com/feed.xml
chill podcasts top --country us
chill podcasts search "science" --json
chill podcasts categories
chill podcasts category "True Crime"
chill podcasts episodes https://example.com/feed.xml
chill podcasts subscribe https://example.com/feed.xml
chill podcasts subscriptions --json
chill podcasts unsubscribe https://example.com/feed.xml
chill podcasts sync
chill podcasts inbox
chill podcasts inbox --played
chill podcasts inbox queue
chill podcasts download https://example.com/feed.xml 3
chill podcasts downloads
chill podcasts downloads retry 2
chill podcasts downloads pin 1
chill podcasts auto on --latest 2 --concurrency 3 --quota 12GB --retain 45
chill podcasts country gb
chill podcasts play https://example.com/feed.xml 3
chill podcasts play https://example.com/feed.xml 3 --restart
chill podcasts latest https://example.com/feed.xml
chill podcasts queue https://example.com/feed.xml 4
chill podcasts queue --json
chill podcasts clear
chill seek -30
chill seek +30
chill --seek 2m
chill speed 1.5
chill next
chill prev
chill pause
chill resume
chill status --json
```

Episode numbers refer to `podcasts episodes` output. Without a number, `play`
and `queue` select the newest published episode, falling back to feed order.
`next` plays the next universal queue item. `prev` restarts finite media after
three seconds of playback, or returns to the previous item near the start.
Podcast episodes can be mixed with stations, local tracks, and URLs; queue,
shuffle, repeat, saved playlist, status, and native-control behavior is shared.

`podcasts --help` lists the commands. `podcast` is also accepted. Listing and
search commands support `--json`; they do not start audio playback. Subscribing
starts an idle daemon if needed so multiple clients share one writer.

## Saved progress

Subscriptions, chart country, playback speed, progress, inbox state, download
settings, transfer state, and integrity digests are saved in `podcasts.json`
beside `stations.json`. The daemon saves progress every 15 seconds and when pausing,
seeking, switching, or stopping. Closing the REPL does not stop these updates.

Returning to an episode resumes five seconds before the saved position. The
first fifteen seconds restart from the beginning. Finished episodes are marked
played; stopping within the final minute also counts, or past halfway for an
episode shorter than two minutes. `--restart` bypasses the saved position.

Episodes are identified by feed and publisher ID, with publication metadata as
a fallback. New signed audio URLs therefore usually retain the same progress.
An unreadable or newer-format save file is left untouched and reported as an
error. The most recent 2,000 episode records are kept.

## Inbox and offline downloads

`podcasts sync` fetches subscribed feeds concurrently and merges their newest
episodes into a date-sorted inbox. The default inbox view is unplayed items;
`--played` selects played items and `--all` includes both. `podcasts inbox queue`
adds the newest matching episode from every show to the universal queue.

Manual downloads use `podcasts download`. Automatic downloads are opt-in with
`podcasts auto on`; `--latest`, `--concurrency`, `--quota`, and `--retain`
control episodes per show, simultaneous transfers, total managed storage, and
played-episode retention. A retention value of zero keeps played downloads
until the quota requires space. Pinning excludes a file from both policies.

Downloads use `.part` files, resume with a validated HTTP byte range, enforce
the configured size ceiling while streaming, fsync before atomic promotion,
and record SHA-256. Interrupted transfers retain their partial data. Transient
transfers make up to five attempts with capped exponential delay, while `downloads
retry` resets a failed item manually. The Downloads screen and CLI expose
queued, downloading, retrying, ready, error, and policy-evicted state plus byte
progress. Evicted records prevent automatic download loops and can be downloaded
again manually.

Playback prefers a ready download only after its path, size, and SHA-256 pass
validation. A missing, modified, or incomplete file is marked as an error and
the episode falls back to its network source. Removing a download cancels an
active transfer and deletes only the matching file inside Chill's managed cache.

## Playback

mpv plays the audio; FFmpeg decodes it and ffprobe measures the episode length.
`chill doctor` checks all three. ffprobe comes with normal FFmpeg installations.
It also checks that the saved podcast settings can be read.
Feed durations are estimates and are replaced with the measured length when
the download finishes.

When no offline copy exists, playback uses a bounded temporary episode cache so
seeking can reuse bytes already received. That temporary cache is removed when
playback switches or stops and is separate from managed offline downloads.
Feed reading is limited to 300 playable episodes and 32 MiB per response; RSS
and Atom audio enclosures are supported.
