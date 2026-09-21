package main

import (
	"errors"
	"fmt"
	"strings"
)

var completionCommands = []string{
	"play", "open", "queue", "playlist", "library", "search", "browse", "providers", "setup", "link", "remote",
	"audio", "device", "mono", "shuffle", "repeat", "favorite", "bookmark", "pause", "resume", "toggle", "stop",
	"status", "volume", "eq", "podcasts", "radio", "history", "lyrics", "notifications", "seek", "speed", "next",
	"prev", "doctor", "add", "remove", "default", "update", "completion",
}

var completionOptions = []string{
	"--fg", "--json", "--station", "--vol", "--seek", "--speed", "--eq", "--shuffle", "--repeat", "--device",
	"--audio-profile", "--sample-rate", "--buffer", "--resample-quality", "--mono", "--mute", "--status", "--stop",
	"--toggle", "--skip", "--sleep", "--list", "--version", "--help",
}

func runCompletionCommand(args []string) (string, error) {
	if helpRequested(args) {
		return "Usage: chill completion <bash|zsh|fish|powershell>", nil
	}
	if len(args) != 1 {
		return "", errors.New("usage: chill completion <bash|zsh|fish|powershell>")
	}
	words := strings.Join(append(append([]string{}, completionCommands...), completionOptions...), " ")
	providers := strings.Join(func() []string {
		keys := make([]string, len(supportedProviders))
		for i, provider := range supportedProviders {
			keys[i] = provider.key
		}
		return keys
	}(), " ")
	switch strings.ToLower(args[0]) {
	case "bash":
		return `_chill_complete() {
  local cur prev command
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"
  command="${COMP_WORDS[1]}"
  if [[ "$command" == remote && "${COMP_WORDS[2]}" == events ]]; then
    COMPREPLY=( $(compgen -W "runtime.state runtime.playback runtime.queue runtime.settings runtime.downloads runtime.metadata runtime.spectrum runtime.job" -- "$cur") )
    return
  fi
  case "$prev" in
    --provider|setup|browse) COMPREPLY=( $(compgen -W "` + providers + `" -- "$cur") );;
    --audio-profile) COMPREPLY=( $(compgen -W "Automatic Lossless Low-Latency Stable-Streaming Custom" -- "$cur") );;
    remote) COMPREPLY=( $(compgen -W "state capabilities call events job cancel" -- "$cur") );;
    audio) COMPREPLY=( $(compgen -W "profile device sample-rate buffer resample-quality mono channels exclusive list" -- "$cur") );;
    device) COMPREPLY=( $(compgen -W "list set default" -- "$cur") );;
    link) COMPREPLY=( $(compgen -W "open register unregister status" -- "$cur") );;
    completion) COMPREPLY=( $(compgen -W "bash zsh fish powershell" -- "$cur") );;
    *) COMPREPLY=( $(compgen -W "` + words + `" -- "$cur") );;
  esac
}
complete -F _chill_complete chill`, nil
	case "zsh":
		return `#compdef chill
_chill() {
  local -a commands providers options values
  commands=(` + strings.Join(completionCommands, " ") + `)
  providers=(` + providers + `)
  options=(` + strings.Join(completionOptions, " ") + `)
  if (( CURRENT == 2 )); then
    _describe 'command' commands
  elif [[ "${words[2]}" == remote && "${words[3]}" == events ]]; then
    values=(runtime.state runtime.playback runtime.queue runtime.settings runtime.downloads runtime.metadata runtime.spectrum runtime.job)
    _describe 'topic' values
  elif [[ "${words[2]}" == remote ]]; then
    values=(state capabilities call events job cancel)
    _describe 'remote command' values
  elif [[ "${words[2]}" == audio ]]; then
    values=(profile device sample-rate buffer resample-quality mono channels exclusive list)
    _describe 'audio setting' values
  elif [[ "${words[2]}" == device ]]; then
    _values 'device command' list set default
  elif [[ "${words[2]}" == link ]]; then
    _values 'link command' open register unregister status
  elif [[ "${words[CURRENT-1]}" == --provider || "${words[2]}" == setup || "${words[2]}" == browse ]]; then
    _describe 'provider' providers
  elif [[ "${words[2]}" == completion ]]; then
    _values 'shell' bash zsh fish powershell
  else
    _describe 'option' options
    _files
  fi
}
compdef _chill chill`, nil
	case "fish":
		var lines []string
		for _, command := range completionCommands {
			lines = append(lines, "complete -c chill -f -n '__fish_use_subcommand' -a "+command)
		}
		for _, option := range completionOptions {
			lines = append(lines, "complete -c chill -l "+strings.TrimPrefix(option, "--"))
		}
		lines = append(lines,
			"complete -c chill -f -n '__fish_seen_subcommand_from setup browse' -a '"+providers+"'",
			"complete -c chill -f -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish powershell'",
			"complete -c chill -f -n '__fish_seen_subcommand_from remote' -a 'state capabilities call events job cancel'",
			"complete -c chill -f -n '__fish_seen_subcommand_from audio' -a 'profile device sample-rate buffer resample-quality mono channels exclusive list'",
			"complete -c chill -f -n '__fish_seen_subcommand_from device' -a 'list set default'",
			"complete -c chill -f -n '__fish_seen_subcommand_from link' -a 'open register unregister status'",
		)
		return strings.Join(lines, "\n"), nil
	case "powershell", "pwsh":
		quoted := make([]string, 0, len(completionCommands)+len(completionOptions))
		for _, word := range append(append([]string{}, completionCommands...), completionOptions...) {
			quoted = append(quoted, "'"+strings.ReplaceAll(word, "'", "''")+"'")
		}
		return `Register-ArgumentCompleter -Native -CommandName chill -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $line = $commandAst.ToString()
  $values = @(` + strings.Join(quoted, ", ") + `)
  if ($line -match '^chill\s+remote\s+events(?:\s|$)') {
    $values = @('runtime.state', 'runtime.playback', 'runtime.queue', 'runtime.settings', 'runtime.downloads', 'runtime.metadata', 'runtime.spectrum', 'runtime.job')
  } elseif ($line -match '^chill\s+remote(?:\s|$)') {
    $values = @('state', 'capabilities', 'call', 'events', 'job', 'cancel')
  } elseif ($line -match '^chill\s+audio(?:\s|$)') {
    $values = @('profile', 'device', 'sample-rate', 'buffer', 'resample-quality', 'mono', 'channels', 'exclusive', 'list')
  } elseif ($line -match '^chill\s+device(?:\s|$)') {
    $values = @('list', 'set', 'default')
  } elseif ($line -match '^chill\s+link(?:\s|$)') {
    $values = @('open', 'register', 'unregister', 'status')
  }
  $values |
    Where-Object { $_ -like "$wordToComplete*" } |
    ForEach-Object { [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_) }
}`, nil
	default:
		return "", fmt.Errorf("unsupported shell %q", args[0])
	}
}
