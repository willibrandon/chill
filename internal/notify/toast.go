package notify

import (
	"encoding/xml"
	"net/url"
	"strings"
)

type toastDocument struct {
	// XMLName fixes the document root.
	XMLName xml.Name `xml:"toast"`
	// Duration requests a transient toast.
	Duration string `xml:"duration,attr"`
	// Visual contains visible notification content.
	Visual toastVisual `xml:"visual"`
	// Audio controls notification sound.
	Audio toastAudio `xml:"audio"`
}

type toastVisual struct {
	// Binding selects and populates the toast template.
	Binding toastBinding `xml:"binding"`
}

type toastBinding struct {
	// Template names the Windows toast template.
	Template string `xml:"template,attr"`
	// Image is optional station artwork.
	Image *toastImage `xml:"image,omitempty"`
	// Text holds the title and secondary line.
	Text []string `xml:"text"`
}

type toastImage struct {
	// Placement selects the app-logo image position.
	Placement string `xml:"placement,attr"`
	// Source identifies the artwork.
	Source string `xml:"src,attr"`
}

type toastAudio struct {
	// Silent prevents track changes from making a sound.
	Silent bool `xml:"silent,attr"`
}

func buildToastXML(title, body, artwork string) string {
	binding := toastBinding{
		Template: "ToastGeneric",
		Text:     []string{cleanXMLText(title), cleanXMLText(body)},
	}
	if source := toastArtworkSource(artwork); source != "" {
		binding.Image = &toastImage{Placement: "appLogoOverride", Source: source}
	}
	document := toastDocument{
		Duration: "short",
		Visual:   toastVisual{Binding: binding},
		Audio:    toastAudio{Silent: true},
	}
	encoded, _ := xml.Marshal(document)
	return string(encoded)
}

func toastArtworkSource(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "file":
		return cleanXMLText(parsed.String())
	default:
		return ""
	}
}

func cleanXMLText(value string) string {
	return strings.Map(func(character rune) rune {
		switch {
		case character == '\t', character == '\n', character == '\r':
			return character
		case character >= 0x20 && character <= 0xd7ff:
			return character
		case character >= 0xe000 && character <= 0xfffd:
			return character
		case character >= 0x10000 && character <= 0x10ffff:
			return character
		default:
			return '\ufffd'
		}
	}, value)
}
