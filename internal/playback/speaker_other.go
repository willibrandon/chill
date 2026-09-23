//go:build !darwin

package playback

// OpenDevice prepares native output without starting sample consumption.
func OpenDevice(settings Settings, render func([]byte)) (Device, error) {
	return openNativeDevice(settings, render)
}
