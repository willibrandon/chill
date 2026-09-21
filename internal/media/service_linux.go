//go:build linux

package media

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

func cleanVolume(volume float64) float64 {
	return min(1, max(0, volume))
}

const mediaPath = dbus.ObjectPath("/org/mpris/MediaPlayer2")

const introspection = `<node>
  <interface name="org.mpris.MediaPlayer2">
    <method name="Raise"/><method name="Quit"/>
    <property name="Identity" type="s" access="read"/>
    <property name="DesktopEntry" type="s" access="read"/>
    <property name="CanQuit" type="b" access="read"/>
    <property name="CanRaise" type="b" access="read"/>
    <property name="HasTrackList" type="b" access="read"/>
    <property name="SupportedUriSchemes" type="as" access="read"/>
    <property name="SupportedMimeTypes" type="as" access="read"/>
  </interface>
  <interface name="org.mpris.MediaPlayer2.Player">
    <method name="Next"/><method name="Previous"/><method name="Pause"/>
    <method name="PlayPause"/><method name="Stop"/><method name="Play"/>
    <method name="Seek"><arg direction="in" type="x"/></method>
    <method name="SetPosition"><arg direction="in" type="o"/><arg direction="in" type="x"/></method>
    <signal name="Seeked"><arg type="x"/></signal>
    <property name="PlaybackStatus" type="s" access="read"/>
    <property name="Rate" type="d" access="read"/>
    <property name="Metadata" type="a{sv}" access="read"/>
    <property name="Volume" type="d" access="readwrite"/>
    <property name="Position" type="x" access="read"/>
    <property name="MinimumRate" type="d" access="read"/>
    <property name="MaximumRate" type="d" access="read"/>
    <property name="CanGoNext" type="b" access="read"/>
    <property name="CanGoPrevious" type="b" access="read"/>
    <property name="CanPlay" type="b" access="read"/>
    <property name="CanPause" type="b" access="read"/>
    <property name="CanSeek" type="b" access="read"/>
    <property name="CanControl" type="b" access="read"/>
  </interface>` + introspect.IntrospectDataString + `</node>`

// Service owns the desktop media session.
type Service struct {
	conn          *dbus.Conn
	props         *prop.Properties
	send          func(Command)
	mu            sync.Mutex
	last          State
	trackID       dbus.ObjectPath
	trackSequence int64
}

type rootInterface struct{ service *Service }

// Raise is present for protocol completeness; the app has no graphical window.
func (r rootInterface) Raise() *dbus.Error { return nil }

// Quit stops playback but leaves the long-running daemon available.
func (r rootInterface) Quit() *dbus.Error {
	r.service.send(Command{Kind: Stop})
	return nil
}

type playerInterface struct{ service *Service }

// Next selects the next item.
func (p playerInterface) Next() *dbus.Error { p.service.send(Command{Kind: Next}); return nil }

// Previous selects the previous item.
func (p playerInterface) Previous() *dbus.Error { p.service.send(Command{Kind: Previous}); return nil }

// Pause pauses playback.
func (p playerInterface) Pause() *dbus.Error { p.service.send(Command{Kind: Pause}); return nil }

// PlayPause toggles the pause state.
func (p playerInterface) PlayPause() *dbus.Error { p.service.send(Command{Kind: Toggle}); return nil }

// Stop stops the current item.
func (p playerInterface) Stop() *dbus.Error { p.service.send(Command{Kind: Stop}); return nil }

// Play starts or resumes playback.
func (p playerInterface) Play() *dbus.Error { p.service.send(Command{Kind: Play}); return nil }

// DoSeek moves the playhead by a signed number of microseconds.
func (p playerInterface) DoSeek(offset int64) *dbus.Error {
	p.service.send(Command{Kind: Seek, Position: time.Duration(offset) * time.Microsecond})
	return nil
}

// SetPosition moves the current track to an absolute microsecond position.
func (p playerInterface) SetPosition(trackID dbus.ObjectPath, position int64) *dbus.Error {
	p.service.mu.Lock()
	current := p.service.trackID
	p.service.mu.Unlock()
	if trackID == current {
		p.service.send(Command{Kind: SetPosition, Position: max(0, time.Duration(position)*time.Microsecond)})
	}
	return nil
}

// New creates and exports a desktop media session.
func New(send func(Command)) (*Service, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("desktop media session: %w", err)
	}
	name := "org.mpris.MediaPlayer2.chill"
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err == nil && reply != dbus.RequestNameReplyPrimaryOwner {
		name = "org.mpris.MediaPlayer2.chill.instance" + strconv.Itoa(os.Getpid())
		reply, err = conn.RequestName(name, dbus.NameFlagDoNotQueue)
	}
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		conn.Close()
		if err != nil {
			return nil, fmt.Errorf("desktop media session: %w", err)
		}
		return nil, fmt.Errorf("desktop media session name is unavailable")
	}
	s := &Service{conn: conn, send: send, trackSequence: 1, trackID: trackPath(1)}
	if err = conn.Export(rootInterface{s}, mediaPath, "org.mpris.MediaPlayer2"); err == nil {
		err = conn.ExportWithMap(playerInterface{s}, map[string]string{"DoSeek": "Seek"}, mediaPath, "org.mpris.MediaPlayer2.Player")
	}
	if err == nil {
		err = conn.Export(introspect.Introspectable(introspection), mediaPath, "org.freedesktop.DBus.Introspectable")
	}
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("desktop media session: %w", err)
	}
	spec := map[string]map[string]*prop.Prop{
		"org.mpris.MediaPlayer2": {
			"Identity": {Value: "Chill", Emit: prop.EmitTrue}, "DesktopEntry": {Value: "chill", Emit: prop.EmitTrue},
			"CanQuit": {Value: true, Emit: prop.EmitTrue}, "CanRaise": {Value: false, Emit: prop.EmitTrue},
			"HasTrackList": {Value: false, Emit: prop.EmitTrue}, "SupportedUriSchemes": {Value: []string{"http", "https"}, Emit: prop.EmitTrue},
			"SupportedMimeTypes": {Value: []string{"audio/mpeg", "audio/aac", "audio/ogg", "application/vnd.apple.mpegurl"}, Emit: prop.EmitTrue},
		},
		"org.mpris.MediaPlayer2.Player": {
			"PlaybackStatus": {Value: string(StatusStopped), Emit: prop.EmitTrue}, "Rate": {Value: 1.0, Emit: prop.EmitTrue},
			"Metadata": {Value: metadata(Track{}, s.trackID), Emit: prop.EmitTrue},
			"Volume": {Value: 0.7, Writable: true, Emit: prop.EmitTrue, Callback: func(change *prop.Change) *dbus.Error {
				if volume, ok := change.Value.(float64); ok {
					s.send(Command{Kind: SetVolume, Volume: cleanVolume(volume)})
				}
				return nil
			}},
			"Position": {Value: int64(0), Emit: prop.EmitFalse}, "MinimumRate": {Value: 1.0, Emit: prop.EmitTrue},
			"MaximumRate": {Value: 1.0, Emit: prop.EmitTrue}, "CanControl": {Value: true, Emit: prop.EmitTrue},
			"CanPlay": {Value: true, Emit: prop.EmitTrue}, "CanPause": {Value: true, Emit: prop.EmitTrue},
			"CanGoNext": {Value: true, Emit: prop.EmitTrue}, "CanGoPrevious": {Value: false, Emit: prop.EmitTrue},
			"CanSeek": {Value: false, Emit: prop.EmitTrue},
		},
	}
	s.props, err = prop.Export(conn, mediaPath, spec)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("desktop media properties: %w", err)
	}
	return s, nil
}

func trackPath(sequence int64) dbus.ObjectPath {
	return dbus.ObjectPath("/org/mpris/MediaPlayer2/Track/" + strconv.FormatInt(sequence, 10))
}

func cleanText(value string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(value, "�"), "\x00", "")
}

func metadata(track Track, id dbus.ObjectPath) map[string]dbus.Variant {
	result := map[string]dbus.Variant{"mpris:trackid": dbus.MakeVariant(id)}
	if v := cleanText(track.Title); v != "" {
		result["xesam:title"] = dbus.MakeVariant(v)
	}
	if v := cleanText(track.Artist); v != "" {
		result["xesam:artist"] = dbus.MakeVariant([]string{v})
	}
	if v := cleanText(track.Album); v != "" {
		result["xesam:album"] = dbus.MakeVariant(v)
	}
	if v := cleanText(track.Genre); v != "" {
		result["xesam:genre"] = dbus.MakeVariant([]string{v})
	}
	if v := cleanText(track.URL); v != "" {
		result["xesam:url"] = dbus.MakeVariant(v)
	}
	if v := cleanText(track.ArtURL); v != "" {
		result["mpris:artUrl"] = dbus.MakeVariant(v)
	}
	if track.Duration > 0 {
		result["mpris:length"] = dbus.MakeVariant(track.Duration.Microseconds())
	}
	return result
}

// Run executes work while servicing any platform main-loop requirements.
func Run(_ *Service, work func() error) error { return work() }

// Update publishes a complete playback snapshot.
func (s *Service) Update(state State) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if state.Status != s.last.Status {
		s.props.SetMust("org.mpris.MediaPlayer2.Player", "PlaybackStatus", string(state.Status))
	}
	if state.Track != s.last.Track {
		s.trackSequence++
		s.trackID = trackPath(s.trackSequence)
		s.props.SetMust("org.mpris.MediaPlayer2.Player", "Metadata", metadata(state.Track, s.trackID))
	}
	if state.Volume != s.last.Volume {
		s.props.SetMust("org.mpris.MediaPlayer2.Player", "Volume", cleanVolume(state.Volume))
	}
	s.props.SetMust("org.mpris.MediaPlayer2.Player", "Position", state.Position.Microseconds())
	if state.Seekable != s.last.Seekable {
		s.props.SetMust("org.mpris.MediaPlayer2.Player", "CanSeek", state.Seekable)
	}
	if state.CanGoNext != s.last.CanGoNext {
		s.props.SetMust("org.mpris.MediaPlayer2.Player", "CanGoNext", state.CanGoNext)
	}
	if state.CanGoPrevious != s.last.CanGoPrevious {
		s.props.SetMust("org.mpris.MediaPlayer2.Player", "CanGoPrevious", state.CanGoPrevious)
	}
	s.last = state
}

// Seeked reports a discontinuous playhead change.
func (s *Service) Seeked(position time.Duration) {
	if s != nil && s.conn != nil {
		_ = s.conn.Emit(mediaPath, "org.mpris.MediaPlayer2.Player.Seeked", position.Microseconds())
	}
}

// Close removes the desktop media session.
func (s *Service) Close() {
	if s != nil && s.conn != nil {
		s.conn.Close()
	}
}
