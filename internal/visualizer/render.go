// Package visualizer renders audio snapshots into bounded terminal canvases.
// It knows nothing about processes, sockets, Bubble Tea, or playback commands.
package visualizer

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/willibrandon/chill/internal/audio"
)

// Modes is the stable order used by the REPL's picker and cycle binding.
var Modes = []string{
	"spectrum", "bars", "mirror", "skyline", "dots", "peaks", "ribbon",
	"waterfall", "spectrogram", "scope", "stereo-scope", "wave", "filled-wave",
	"vectorscope", "lissajous", "orbit", "rings", "radar", "spiral",
	"particles", "rain", "matrix", "flame", "embers", "pulse", "diamonds",
	"tunnel", "starburst", "led", "vu", "balance",
}

// Index returns a mode's position in Modes, or -1 if the name is unknown.
func Index(name string) int {
	for i, mode := range Modes {
		if mode == name {
			return i
		}
	}
	return -1
}

// Renderer owns only presentation state: attack/release, peak hold, and a
// fixed-size history. Advance is separate from Render, so resize/redraw cannot
// advance animation. The subscriber, rather than terminal redraws, drives time.
type Renderer struct {
	// Frame is the most recently supplied audio snapshot.
	Frame     audio.Frame
	levels    [audio.Bands]float64
	peaks     [audio.Bands]float64
	history   [120][audio.Bands]float64
	head      int
	steps     uint64
	last      time.Time
	hold      [2]float64
	holdUntil [2]time.Time
}

// Reset clears audio, smoothing, peak holds, and animation history.
func (r *Renderer) Reset() { *r = Renderer{} }

// Level maps a linear amplitude onto a clamped -60 to 0 dB display scale.
func Level(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return min(1, max(0, (20*math.Log10(x)+60)/60))
}

// Advance updates presentation state using an audio frame and elapsed time.
func (r *Renderer) Advance(f audio.Frame, now time.Time) {
	dt := 1.0 / 30
	if !r.last.IsZero() {
		dt = min(0.25, max(0, now.Sub(r.last).Seconds()))
	}
	r.last = now
	r.Frame = f
	for i, x := range f.Spectrum {
		target := Level(x)
		tau := 0.18
		if target > r.levels[i] {
			tau = 0.035
		}
		r.levels[i] += (target - r.levels[i]) * (1 - math.Exp(-dt/tau))
		r.peaks[i] = max(r.levels[i], r.peaks[i]-dt*0.3)
	}
	for ch := range 2 {
		peak := Level(f.Peak[ch])
		if peak >= r.hold[ch] {
			r.hold[ch], r.holdUntil[ch] = peak, now.Add(750*time.Millisecond)
		} else if now.After(r.holdUntil[ch]) {
			r.hold[ch] = max(peak, r.hold[ch]-dt*0.5)
		}
	}
	r.history[r.head] = r.levels
	r.head = (r.head + 1) % len(r.history)
	r.steps++
}

type cell struct {
	char  rune
	color int
}
type canvas struct {
	width, height int
	cells         []cell
}

func (c *canvas) put(x, y int, char rune, color int) {
	if x >= 0 && x < c.width && y >= 0 && y < c.height {
		c.cells[y*c.width+x] = cell{char, color}
	}
}

func (c *canvas) line(x0, y0, x1, y1 int, char rune, color int) {
	dx, dy := int(math.Abs(float64(x1-x0))), -int(math.Abs(float64(y1-y0)))
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		c.put(x0, y0, char, color)
		if x0 == x1 && y0 == y1 {
			break
		}
		e := 2 * err
		if e >= dy {
			err += dy
			x0 += sx
		}
		if e <= dx {
			err += dx
			y0 += sy
		}
	}
}

var colors = []string{"\x1b[38;5;238m", "\x1b[38;5;60m", "\x1b[38;5;67m", "\x1b[38;5;74m", "\x1b[38;5;80m", "\x1b[38;5;116m", "\x1b[38;5;159m", "\x1b[38;5;231m", "\x1b[38;5;114m", "\x1b[38;5;220m", "\x1b[38;5;203m"}

// String encodes the canvas as fixed-width rows with ANSI foreground colors.
func (c *canvas) String() string {
	var b strings.Builder
	for y := range c.height {
		color := -1
		for x := range c.width {
			p := c.cells[y*c.width+x]
			if p.char == 0 {
				b.WriteByte(' ')
				continue
			}
			if p.color != color {
				b.WriteString(colors[p.color%len(colors)])
				color = p.color
			}
			b.WriteRune(p.char)
		}
		if color != -1 {
			b.WriteString("\x1b[0m")
		}
		if y < c.height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Render is deterministic and read-only. Size caps bound CPU and allocation
// even for unusually large terminal resize events.
func (r *Renderer) Render(mode string, width, height int) string {
	w, h := min(512, max(0, width)), min(160, max(0, height))
	if w == 0 || h == 0 {
		return ""
	}
	c := canvas{width: w, height: h, cells: make([]cell, w*h)}
	switch mode {
	case "scope", "stereo-scope", "wave", "filled-wave":
		r.scope(&c, mode)
	case "waterfall", "spectrogram", "ribbon":
		r.waterfall(&c, mode)
	case "vectorscope", "lissajous":
		r.vector(&c, mode)
	case "led", "vu", "balance":
		r.meter(&c, mode)
	case "matrix", "rain", "particles", "flame", "embers":
		r.field(&c, mode)
	case "orbit", "rings", "radar", "spiral", "pulse", "diamonds", "tunnel", "starburst":
		r.radial(&c, mode)
	default:
		r.spectrum(&c, mode)
	}
	return c.String()
}

func (r *Renderer) spectrum(c *canvas, mode string) {
	stride := 2
	if mode == "skyline" || mode == "mirror" {
		stride = 1
	}
	for x := 0; x < c.width; x += stride {
		i := min(audio.Bands-1, x*audio.Bands/c.width)
		n := int(r.levels[i] * float64(c.height) * 8)
		for y := 0; y < c.height; y++ {
			units := min(8, max(0, n-y*8))
			if units == 0 {
				continue
			}
			char := []rune(" ▁▂▃▄▅▆▇█")[units]
			if mode == "bars" {
				char = '▪'
			}
			if mode == "dots" {
				if y%2 != 0 {
					continue
				}
				char = '•'
			}
			if mode == "peaks" && y != (n-1)/8 {
				continue
			}
			row := c.height - 1 - y
			if mode == "mirror" {
				row = c.height/2 - y/2
				c.put(x, c.height/2+y/2, char, min(7, 2+y*5/c.height))
			}
			c.put(x, row, char, min(7, 2+y*5/c.height))
		}
		if mode == "spectrum" && r.peaks[i] > 0 {
			c.put(x, c.height-1-int(r.peaks[i]*float64(c.height-1)), '─', 7)
		}
	}
}

func (r *Renderer) scope(c *canvas, mode string) {
	channels := 1
	if mode == "stereo-scope" {
		channels = 2
	}
	for ch := range channels {
		top, height := ch*c.height/channels, max(1, c.height/channels)
		center := top + height/2
		last := center
		for x := range c.width {
			i := x * (audio.WaveSize - 1) / max(1, c.width-1)
			value := r.Frame.Wave[ch][i]
			if mode == "wave" || mode == "filled-wave" {
				value = (value + r.Frame.Wave[1][i]) / 2
			}
			y := center - int(max(-1, min(1, value))*float64(max(1, height-1))/2)
			c.put(x, center, '·', 0)
			if mode == "filled-wave" {
				c.line(x, center, x, y, '│', 3)
			}
			if x > 0 && mode != "wave" {
				c.line(x-1, last, x, y, '•', 6+ch)
			} else {
				c.put(x, y, '•', 6+ch)
			}
			last = y
		}
	}
}

func (r *Renderer) waterfall(c *canvas, mode string) {
	for y := range c.height {
		for x := range c.width {
			age, band := y, x*audio.Bands/c.width
			if mode == "spectrogram" {
				age, band = c.width-1-x, (c.height-1-y)*audio.Bands/c.height
			}
			if age >= len(r.history) {
				continue
			}
			v := r.history[(r.head-1-age+len(r.history))%len(r.history)][band]
			if v < 0.03 {
				continue
			}
			if mode == "ribbon" {
				if y != int((1-v)*float64(c.height-1)) {
					continue
				}
				c.put(x, y, '~', 6)
			} else {
				c.put(x, y, []rune(" ░▒▓█")[min(4, int(v*5))], min(7, 1+int(v*6)))
			}
		}
	}
}

func (r *Renderer) vector(c *canvas, mode string) {
	for i := range audio.WaveSize {
		l, rr := r.Frame.Wave[0][i], r.Frame.Wave[1][i]
		x, y := l, rr
		if mode == "vectorscope" {
			x, y = (l-rr)/2, (l+rr)/2
		}
		c.put(int((1+max(-1, min(1, x)))*float64(c.width-1)/2), int((1-max(-1, min(1, y)))*float64(c.height-1)/2), '•', 3+i*4/audio.WaveSize)
	}
}

func (r *Renderer) meter(c *canvas, mode string) {
	if mode == "balance" {
		total := r.Frame.RMS[0] + r.Frame.RMS[1]
		if total > 0 {
			x := int(r.Frame.RMS[1] / total * float64(c.width-1))
			c.line(c.width/2, c.height/2, x, c.height/2, '━', 6)
			c.put(x, c.height/2, '◆', 7)
		}
		return
	}
	for ch := range 2 {
		y := min(c.height-1, (ch*2+1)*c.height/4)
		reading := r.Frame.Peak[ch]
		if mode == "vu" {
			reading = r.Frame.RMS[ch]
		}
		label := fmt.Sprintf("%s %5.1f dBFS", []string{"L", "R"}[ch], 20*math.Log10(max(0.000001, reading)))
		for x, char := range label {
			c.put(x, y, char, 7)
		}
		start, width := 15, max(1, c.width-16)
		v := Level(reading)
		for x := range width {
			color, char := 0, '░'
			if float64(x)/float64(width) < v {
				color, char = 8, '▰'
				if float64(x)/float64(width) >= 0.8 {
					color = 9
				}
				if float64(x)/float64(width) >= 0.95 {
					color = 10
				}
			}
			c.put(start+x, y, char, color)
		}
		if r.hold[ch] > 0 {
			c.put(start+int(r.hold[ch]*float64(width-1)), y, '│', 7)
		}
	}
}

func (r *Renderer) field(c *canvas, mode string) {
	for x := range c.width {
		v := r.levels[x*audio.Bands/c.width]
		if v < 0.03 {
			continue
		}
		n := int(v * float64(c.height))
		for j := 0; j < n; j++ {
			y := c.height - 1 - j
			color, char := min(7, 2+j*5/max(1, n)), '▓'
			hash := uint64(x*73856093+j*19349663) + r.steps*83492791
			switch mode {
			case "matrix", "rain":
				if x%2 != 0 {
					continue
				}
				y = (int(r.steps/2) + x*7 - j + c.height*2) % c.height
				color = 8
				char = []rune("01:¦+")[hash%5]
				if mode == "rain" {
					char = '│'
				}
				if j == 0 {
					color = 7
				}
			case "particles":
				if hash%7 != 0 {
					continue
				}
				y = int(hash % uint64(c.height))
				char = '·'
			case "flame", "embers":
				color = 10
				if j < n/2 {
					color = 9
				}
				if j < n/4 {
					color = 7
				}
				if mode == "embers" {
					if hash%4 != 0 {
						continue
					}
					char = '•'
				} else if j > n-3 && hash%3 == 0 {
					continue
				}
			}
			c.put(x, y, char, color)
		}
	}
}

func (r *Renderer) radial(c *canvas, mode string) {
	energy := 0.0
	for _, v := range r.levels {
		energy += v / audio.Bands
	}
	if energy < 0.01 {
		return
	}
	cx, cy := float64(c.width-1)/2, float64(c.height-1)/2
	for i := range 240 {
		a := float64(i) * 2 * math.Pi / 240
		band := i * audio.Bands / 240
		radius := 0.2 + 0.75*r.levels[band]
		rotation := float64(r.steps) * 0.025
		switch mode {
		case "pulse":
			radius = energy
		case "rings":
			radius = float64(i%3+1) / 3 * energy
			a *= 3
		case "orbit":
			radius = 0.5 + energy/2
			a += rotation
		case "radar":
			a = a/4 + rotation
		case "spiral":
			radius *= float64(i) / 240
			a = a*3 + rotation
		case "diamonds":
			radius /= math.Abs(math.Cos(a)) + math.Abs(math.Sin(a))
		case "tunnel":
			radius = float64(i%5+1) / 5 * energy
			a = a*5 + rotation
		}
		x, y := int(cx+math.Cos(a)*cx*radius), int(cy+math.Sin(a)*cy*radius)
		if mode == "starburst" {
			c.line(int(cx), int(cy), x, y, '·', 2+band%5)
		} else {
			c.put(x, y, '•', 2+band%6)
		}
	}
}
