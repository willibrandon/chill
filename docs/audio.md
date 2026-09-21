# Audio output

Chill sends decoded float PCM through the equalizer and visualizer before mpv
opens the selected output device. Output settings apply to stations, podcasts,
local media, direct URLs, and provider tracks in daemon and foreground mode.

## Profiles

| Profile | Sample rate | Buffer | Resampling |
| --- | ---: | ---: | ---: |
| Automatic | 48 kHz | 100 ms | 3 |
| Lossless | 96 kHz | 250 ms | 4 |
| Low Latency | 48 kHz | 50 ms | 2 |
| Stable Streaming | 48 kHz | 2000 ms | 3 |

Changing an individual sample rate, buffer, or resampling value selects Custom.
Supported sample rates are 44.1, 48, 88.2, 96, 176.4, and 192 kHz. Buffer size
ranges from 50 to 5000 milliseconds and resampling quality from 1 to 4.

```sh
chill audio
chill audio profile Lossless
chill audio sample-rate 96000
chill audio buffer 250
chill audio resample-quality 4
chill audio channels mono
chill audio exclusive on
chill mono toggle
```

The equivalent startup flags are `--audio-profile`, `--sample-rate`, `--buffer`,
`--resample-quality`, `--mono`, and `--device`. They work with ordinary daemon
playback and `--fg`.

## Devices

`chill device list` reports mpv's cross-platform output identifiers and marks
the selected preference. `chill device set <id-or-name>` accepts an exact id,
an exact display name, or one unambiguous partial match. `chill device default`
returns to automatic system routing.

Device-only changes are applied without restarting the decoder. Format changes
restart the PCM pipeline at the same finite-media position and retain the
current item and queue. If a remembered device disappears, Chill switches to
`auto`; it switches back when that device is available again.

Press **F9** for the live settings screen. Enter chooses a profile or device.
Left and right cycle advanced values, and `Ctrl+R` refreshes device discovery.

`status --json` includes desired settings, active device, active sample rate,
and the PCM format. `chill doctor` checks device enumeration and reports an
unavailable saved selection.
