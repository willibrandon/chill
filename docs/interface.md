# Terminal interface

Chill uses one presentation profile across the interactive REPL, foreground
playback, command output, and remote state. Press `F10` to preview changes
without leaving the current session. Press `s` to save the preview or `Esc` to
restore the previous appearance.

## Themes and color

The built-in themes are Midnight, Paper, High Contrast, Monochrome, and
Colorblind Dark. Each palette is checked for readable text, selection, muted
text, and status-bar contrast before it can be used.

```sh
chill theme list
chill theme preview Paper
chill theme set "High Contrast"
chill theme validate
chill --theme Paper
chill --no-color
```

`--theme` and `--no-color` apply to the current invocation. `chill theme set`
persists the choice. `NO_COLOR` is also honored.

User themes are JSON files in the `themes` directory beside `interface.json`.
The file name is not significant. A complete theme has this shape:

```json
{
  "name": "Ocean",
  "description": "Cool colors on a dark background",
  "background": "#08131A",
  "foreground": "#E8F3F8",
  "bright": "#FFFFFF",
  "muted": "#A1B4BD",
  "accent": "#55C2FF",
  "secondary": "#6DE1C2",
  "success": "#75D69C",
  "warning": "#F5C85B",
  "error": "#FF8794",
  "selection_foreground": "#08131A",
  "selection_background": "#A4DDFF",
  "border": "#778B94",
  "status_foreground": "#08131A",
  "status_background": "#E8F3F8"
}
```

Colors must use `#RRGGBB`. Normal, bright, selection, and status text require a
WCAG contrast ratio of at least 4.5:1. Muted and accent text require at least
3:1. Invalid files are reported by `chill theme validate`; an invalid saved
selection falls back safely to Midnight.

Color capability can be `auto`, `truecolor`, `ansi256`, `ansi16`, or `none`.
Character capability can be `auto`, `unicode`, or `ascii`. ASCII mode replaces
decorative box, graph, arrow, and music glyphs with readable ASCII equivalents.

## Adaptive layouts and accessibility

The interface selects a full layout on large terminals, a content-first layout
for browsers and medium terminals, and a minimal layout for small terminals.
Below 40 columns or 10 rows it shows a resize message instead of clipping
controls. Full layouts can show source, queue, equalizer, audio, download,
network, and metadata panels. Each panel can be disabled independently.

Simplified mode leaves the alternate screen, mouse tracking, visualizers,
suggestion popovers, and decorative secondary content disabled. It
is suitable for screen readers and restrained SSH sessions. Low-power mode
limits terminal redraws, polls status less often,
and disables the visualizer. Both can be enabled for one session or persisted:

```sh
chill --simplified
chill --low-power --fg album.m3u
chill interface set simplified on
chill interface set low-power on
chill interface panel metadata off
```

`chill interface show` reports the current profile. `chill interface set`
also configures `colors`, `characters`, `visualizer-height`, `show-status`,
`help-hints`, `status-fields`, `seek-step`, `seek-large-step`,
`initial-directory`, and `default-screen`. `status-fields` is a comma-separated
list drawn from `state`, `position`, `title`, `queue`, `shuffle`, `repeat`,
`volume`, `muted`, `equalizer`, `network`, `audio`, `speed`, `sleep`, and
`storage-error`. Active shuffle, repeat, mute, and storage-error indicators are
always retained so playback modes, silence, and persistence failures remain
visible.

The downloads panel reports ready, downloading, queued, retrying, and failed
states directly. Its snapshot refreshes while the panel is visible, including
changes made by another client.

## Keybindings

Press `Ctrl+K` for a searchable overlay generated from the active action
registry. It shows global commands plus the bindings for the current screen.
Press `/` to search it.

```sh
chill keys list
chill keys search favorite
chill keys set global.library ctrl+g
chill keys set playback.seek-back h
chill keys reset global.library
chill keys conflicts
```

Actions have explicit scopes such as `global`, `prompt`, `library`, `radio`,
`podcasts`, `providers`, `audio`, `equalizer`, `visualizer`, `playback`, and
provider setup. On-screen hints use the active registry, so they change with
the bindings. Chill rejects duplicate effective bindings in a scope and
conflicts with global bindings. Defaults stop working when an action is
remapped, so a remap never leaves a hidden second shortcut behind.
Global remaps require a modifier or function key, preserving ordinary typing
and cursor movement at the command prompt.

## Storage and automation

Settings are stored in `interface.json` under the platform user configuration
directory. The file is written atomically with owner-only permissions. Theme
files live in its sibling `themes` directory.

`chill remote state` includes the complete interface profile. Automation can
read or update it with `settings.interface`; updates use the same validation as
the CLI and F10 screen.

```sh
chill remote call settings.interface --params '{"theme":"Paper","simplified":true}' --wait
```
