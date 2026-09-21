# Remote control

Chill exposes a versioned JSON API over the same local control transport as the
CLI. Unix sockets are mode `0600`. Windows uses a named pipe whose access list
contains only the current user and the system account. The API does not listen
on a TCP port.

## Commands

```sh
chill remote state
chill remote capabilities
chill remote call queue.append --params '{"items":[...]}' --wait
chill remote job <id>
chill remote cancel <id>
chill remote events runtime.playback runtime.queue
```

Requests contain `version`, `id`, and `method`. Operation requests also contain
`operation` and an optional `params` object. Responses echo the request id and
contain either an operation result or an error with a stable code. Current
codes include `invalid_request`, `invalid_version`, `invalid_params`,
`unknown_operation`, `not_found`, `conflict`, `canceled`, `unavailable`, and
`internal_error`.

`state.get` returns playback, queue, settings, library, podcast downloads, and
provider capabilities in one snapshot. `capabilities` returns every supported
operation and event topic. Long-running operations are jobs with queued,
running, succeeded, failed, or canceled state, progress from 0 to 1, and a
bounded retention window.

Queue mutations accept `if_revision`. A stale revision returns `conflict`
instead of silently overwriting another client's edit. Revisions advance for
CLI, TUI, remote, native-control, gapless, and automatic queue transitions and
survive daemon upgrades.

## Events

Subscriptions remain open and emit newline-delimited JSON. Topics are
`runtime.state`, `runtime.playback`, `runtime.queue`, `runtime.settings`,
`runtime.downloads`, `runtime.metadata`, `runtime.spectrum`, and `runtime.job`.
State is retained, spectrum frames are capped at the TUI refresh rate, and a
slow subscriber receives `system.overflow` with `resync_required: true` before
the connection closes.

## Links

Register the per-user handler with `chill link register`; inspect it with
`chill link status` and remove it with `chill link unregister`.

```text
chill://play?target=https%3A%2F%2Fexample.com%2Fsong.flac
chill://queue?provider=youtube&id=VIDEO_ID&next=true
chill://play?provider=ytmusic&q=ambient
chill://queue?provider=navidrome&album=ALBUM_ID
chill://queue?provider=spotify&playlist=PLAYLIST_ID
```

Links reject unknown or repeated parameters, credentials in URLs, control
characters, fragments, oversized input, ambiguous provider selectors, and
provider-page hosts that do not match the selected public provider.

## Shell completion

Generate completion source for Bash, Zsh, Fish, or PowerShell:

```sh
chill completion bash
chill completion zsh
chill completion fish
chill completion powershell
```

Evaluate the output for the current shell or save it in that shell's standard
completion directory. Generated definitions include top-level commands,
options, providers, audio profiles, and completion targets.
