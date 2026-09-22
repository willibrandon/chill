package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var supportedProviders = []struct {
	key  string
	name string
}{
	{"youtube", "YouTube"}, {"ytmusic", "YouTube Music"}, {"soundcloud", "SoundCloud"}, {"mixcloud", "Mixcloud"},
	{"navidrome", "Navidrome"}, {"subsonic", "Subsonic"}, {"plex", "Plex"}, {"jellyfin", "Jellyfin"},
	{"emby", "Emby"}, {"audiobookshelf", "Audiobookshelf"}, {"spotify", "Spotify"}, {"tidal", "Tidal"}, {"qobuz", "Qobuz"},
}

type providerSetupField struct {
	key         string
	label       string
	placeholder string
	secret      bool
	input       textinput.Model
}

type providerSetupValidation struct{ err error }

type providerSetupModel struct {
	document     providerDocument
	secrets      providerSecretsDocument
	provider     string
	fields       []providerSetupField
	selected     int
	picker       bool
	checking     bool
	done         bool
	saved        bool
	embedded     bool
	width        int
	height       int
	note         string
	presentation interfaceSettings
}

func runProviderSetup(initial string) (string, error) {
	document, secrets, err := loadProviderDocuments()
	if err != nil {
		return "", err
	}
	initial = strings.ToLower(strings.TrimSpace(initial))
	if initial != "" && !supportedProvider(initial) {
		return "", fmt.Errorf("unknown provider %q", initial)
	}
	model := providerSetupModel{document: document, secrets: secrets, provider: initial, picker: initial == "", presentation: currentInterfaceSettings()}
	if !model.picker {
		model.openForm(initial)
	}
	result, err := tea.NewProgram(model, interfaceProgramOptions(model.presentation)...).Run()
	if err != nil {
		return "", err
	}
	finished, ok := result.(providerSetupModel)
	if !ok || !finished.saved {
		return "provider setup canceled", nil
	}
	return finished.provider + " settings saved", nil
}

func supportedProvider(key string) bool {
	return slices.ContainsFunc(supportedProviders, func(provider struct{ key, name string }) bool { return provider.key == key })
}

// Init starts the provider setup form.
func (model providerSetupModel) Init() tea.Cmd { return nil }

// Update applies input and asynchronous connection validation results.
func (model providerSetupModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		model.width, model.height = message.Width, message.Height
		for i := range model.fields {
			model.fields[i].input.SetWidth(min(64, max(20, model.width-24)))
		}
		return model, nil
	case providerSetupValidation:
		model.checking = false
		if message.err != nil {
			model.note = message.err.Error()
			return model, nil
		}
		if err := saveProviderDocuments(model.document, model.secrets); err != nil {
			model.note = err.Error()
			return model, nil
		}
		resetProviders()
		if isDaemonRunning() {
			if _, err := ask("providers-reload"); err != nil {
				model.note = "settings saved, but the daemon could not reload them: " + err.Error()
				return model, nil
			}
		}
		model.saved, model.done = true, true
		if model.embedded {
			return model, nil
		}
		return model, tea.Quit
	case tea.KeyPressMsg:
		if model.checking {
			if model.presentation.mapKey("setup-form", message.String()) == "ctrl+c" {
				return model, tea.Quit
			}
			return model, nil
		}
		if model.picker {
			return model.updatePicker(message)
		}
		return model.updateForm(message)
	}
	return model, nil
}

func (model providerSetupModel) updatePicker(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch model.presentation.mapKey("setup-picker", key.String()) {
	case "ctrl+c", "q", "esc":
		if model.embedded {
			model.done = true
			return model, nil
		}
		return model, tea.Quit
	case "up", "k":
		model.selected = max(0, model.selected-1)
	case "down", "j":
		model.selected = min(len(supportedProviders)-1, model.selected+1)
	case "enter":
		model.openForm(supportedProviders[model.selected].key)
	case "d", "delete":
		provider := supportedProviders[model.selected]
		config := model.document.Providers[provider.key]
		config.Type, config.Enabled = provider.key, false
		model.document.Providers[provider.key] = config
		if err := saveProviderDocuments(model.document, model.secrets); err != nil {
			model.note = err.Error()
		} else {
			resetProviders()
			model.note = provider.name + " disabled"
			if isDaemonRunning() {
				if _, err := ask("providers-reload"); err != nil {
					model.note += ", but the daemon could not reload it: " + err.Error()
				}
			}
		}
	}
	return model, nil
}

func (model *providerSetupModel) openForm(key string) {
	model.provider, model.picker, model.selected, model.note = key, false, 0, ""
	config, secret := model.document.Providers[key], model.secrets.Providers[key]
	if config.Type == "" {
		config.Type = key
	}
	add := func(fieldKey, label, placeholder, value string, protected bool) {
		input := textinput.New()
		input.Prompt = ""
		input.Placeholder = placeholder
		input.SetValue(value)
		input.SetVirtualCursor(false)
		input.CharLimit = 4096
		if model.width > 0 {
			input.SetWidth(min(64, max(20, model.width-24)))
		}
		if protected {
			input.EchoMode = textinput.EchoPassword
			input.EchoCharacter = '•'
		}
		configureInterfaceInput(&input)
		model.fields = append(model.fields, providerSetupField{key: fieldKey, label: label, placeholder: placeholder, secret: protected, input: input})
	}
	switch key {
	case "youtube", "ytmusic", "soundcloud", "mixcloud":
		add("cookies", "Browser cookies", "optional: firefox, chrome, safari, edge", config.CookiesFrom, false)
	case "navidrome", "subsonic":
		add("url", "Server URL", "https://music.example.com", config.URL, false)
		add("username", "Username", "account name", config.Username, false)
		add("password", "Password", "password", secret.Password, true)
		add("token", "API token", "optional alternative to password", secret.Token, true)
	case "jellyfin", "emby":
		add("url", "Server URL", "https://media.example.com", config.URL, false)
		add("user_id", "User ID", "optional server user id", config.UserID, false)
		add("token", "Access token", "API access token", secret.Token, true)
	case "plex":
		add("url", "Server URL", "https://server:32400", config.URL, false)
		add("token", "Access token", "X-Plex-Token", secret.Token, true)
	case "audiobookshelf":
		add("url", "Server URL", "https://books.example.com", config.URL, false)
		add("user_id", "User ID", "optional server user id", config.UserID, false)
		add("token", "Access token", "API access token", secret.Token, true)
	case "spotify", "tidal", "qobuz":
		add("url", "API URL", providerAPIURL(key), firstNonempty(config.URL, providerAPIURL(key)), false)
		if key == "qobuz" {
			add("client_id", "App ID", "Qobuz application id", config.ClientID, false)
		}
		add("token", "Access token", "account access token", secret.Token, true)
	}
	if len(model.fields) > 0 {
		model.fields[0].input.Focus()
	}
}

func providerAPIURL(key string) string {
	return map[string]string{"spotify": "https://api.spotify.com", "tidal": "https://openapi.tidal.com", "qobuz": "https://www.qobuz.com/api.json/0.2"}[key]
}

func (model providerSetupModel) updateForm(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyName := model.presentation.mapKey("setup-form", key.String())
	switch keyName {
	case "ctrl+c", "esc":
		if model.embedded {
			model.done = true
			return model, nil
		}
		return model, tea.Quit
	case "tab", "down", "enter":
		if keyName == "enter" && model.selected == len(model.fields)-1 {
			return model.beginValidation()
		}
		model.focus((model.selected + 1) % len(model.fields))
		return model, nil
	case "shift+tab", "up":
		model.focus((model.selected - 1 + len(model.fields)) % len(model.fields))
		return model, nil
	case "ctrl+s":
		return model.beginValidation()
	}
	var command tea.Cmd
	model.fields[model.selected].input, command = model.fields[model.selected].input.Update(key)
	return model, command
}

func (model *providerSetupModel) focus(index int) {
	model.fields[model.selected].input.Blur()
	model.selected = index
	model.fields[model.selected].input.Focus()
}

func (model providerSetupModel) beginValidation() (tea.Model, tea.Cmd) {
	config := model.document.Providers[model.provider]
	secret := model.secrets.Providers[model.provider]
	config.Type, config.Enabled = model.provider, true
	for _, field := range model.fields {
		value := normalizedProviderSetupValue(field.key, field.input.Value())
		switch field.key {
		case "url":
			config.URL = value
		case "username":
			config.Username = value
		case "user_id":
			config.UserID = value
		case "client_id":
			config.ClientID = value
		case "cookies":
			config.CookiesFrom = value
		case "password":
			secret.Password = value
		case "token":
			secret.Token = value
		}
	}
	model.document.Providers[model.provider] = config
	model.secrets.Providers[model.provider] = secret
	model.checking, model.note = true, "validating connection…"
	providerKey := model.provider
	return model, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var provider mediaProvider
		if slices.Contains([]string{"youtube", "ytmusic", "soundcloud", "mixcloud"}, providerKey) {
			provider = newYTDLPProvider(providerKey, config)
		} else {
			provider = newHTTPMediaProvider(providerKey, config, secret)
		}
		return providerSetupValidation{err: provider.validate(ctx)}
	}
}

func normalizedProviderSetupValue(key, value string) string {
	if key == "password" {
		return value
	}
	return strings.TrimSpace(value)
}

// View renders the provider picker or protected setup form.
func (model providerSetupModel) View() tea.View {
	view := tea.View{AltScreen: true}
	if model.done {
		return model.presentation.decorateView(view, model.width)
	}
	var body strings.Builder
	body.WriteString(styleTitle.Bold(true).Render("chill provider setup"))
	body.WriteString("\n\n")
	if model.picker {
		body.WriteString(styleDim.Render("Choose a provider. " + model.presentation.bindingHint("setup-picker.select", "configures it") + "; " + model.presentation.bindingHint("setup-picker.disable", "disables it") + "."))
		body.WriteString("\n\n")
		room := len(supportedProviders)
		if model.height > 0 {
			room = max(1, model.height-7)
		}
		first := max(0, min(model.selected-room/2, len(supportedProviders)-room))
		for i := first; i < min(len(supportedProviders), first+room); i++ {
			provider := supportedProviders[i]
			marker, style := "  ", styleInput
			if i == model.selected {
				marker, style = "› ", styleSelected
			}
			state := "not configured"
			if config, ok := model.document.Providers[provider.key]; ok {
				if config.Enabled {
					state = "enabled"
				} else {
					state = "disabled"
				}
			}
			fmt.Fprintf(&body, "%s%-18s %s\n", marker, style.Render(provider.name), styleDim.Render(state))
		}
	} else {
		name := model.provider
		for _, provider := range supportedProviders {
			if provider.key == model.provider {
				name = provider.name
			}
		}
		body.WriteString(styleHeading.Render(name))
		body.WriteString("\n\n")
		for i, field := range model.fields {
			marker := "  "
			if i == model.selected {
				marker = "› "
			}
			fmt.Fprintf(&body, "%s%-18s %s\n", marker, field.label, field.input.View())
		}
		body.WriteString("\n")
		body.WriteString(styleDim.Render(strings.Join([]string{model.presentation.bindingHint("setup-form.next", "moves"), model.presentation.bindingHint("setup-form.save", "validates and saves"), model.presentation.bindingHint("setup-form.cancel", "cancels")}, " · ")))
	}
	if model.note != "" {
		body.WriteString("\n\n")
		if model.checking {
			body.WriteString(styleDim.Render(model.note))
		} else {
			body.WriteString(styleError.Render(model.note))
		}
	}
	if model.width > 0 {
		content := lipgloss.NewStyle().Width(model.width).Padding(1, 2).Render(body.String())
		if model.height > 0 {
			lines := strings.Split(content, "\n")
			content = strings.Join(lines[:min(len(lines), model.height)], "\n")
		}
		view.SetContent(content)
		return model.presentation.decorateView(view, model.width)
	}
	view.SetContent(body.String())
	return model.presentation.decorateView(view, model.width)
}
