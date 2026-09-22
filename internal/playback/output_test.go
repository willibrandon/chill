package playback

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"time"
)

type manualDevice struct {
	settings       Settings
	starts, closes int
	startErr       error
	latency        time.Duration
}

// Start begins consuming PCM samples.
func (d *manualDevice) Start() error { d.starts++; return d.startErr }

// Close releases resources and waits for owned workers to stop.
func (d *manualDevice) Close() { d.closes++ }

// Info returns the effective output configuration.
func (d *manualDevice) Info() DeviceInfo {
	return DeviceInfo{ID: d.settings.Device, SampleRate: d.settings.SampleRate, Latency: d.latency}
}

// TestCanceledPreloadCannotReachOutput checks canceled samples and track clocks at handoff.
func TestCanceledPreloadCannotReachOutput(t *testing.T) {
	o, render := newManualOutput(t)
	ctx, cancel := context.WithCancel(t.Context())
	if err := o.Write(ctx, testPCM(128), 1, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := o.Write(t.Context(), testPCM(64), 2, time.Second, 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	out := make([]byte, 64*8)
	render(out)
	if id, position := o.Position(); id != 2 || position != time.Second+64 {
		t.Fatalf("stale samples reached output: %d %s", id, position)
	}
	if o.read.Load() != o.Written() {
		t.Fatal("canceled preload remained queued")
	}
}

// TestDeviceStartFailureRestoresOutput checks both successful and failed restoration.
func TestDeviceStartFailureRestoresOutput(t *testing.T) {
	for _, failRestore := range []bool{false, true} {
		var devices []*manualDevice
		o, err := NewOutput(Settings{Device: "auto", SampleRate: 48000, BufferMS: 50}, func(s Settings, _ func([]byte)) (Device, error) {
			d := &manualDevice{settings: s}
			if s.Device == "broken" || failRestore && len(devices) > 1 {
				d.startErr = errors.New("start failed")
			}
			devices = append(devices, d)
			return d, nil
		}, 100, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := o.SetDevice("broken"); err == nil {
			t.Fatal("start failure hidden")
		}
		if (o.Err() != nil) != failRestore {
			t.Fatalf("unexpected output health: %v", o.Err())
		}
		o.Close()
		for _, device := range devices {
			if device.closes != 1 {
				t.Fatalf("device closed %d times", device.closes)
			}
		}
	}
}

// TestDrainIncludesDeviceBuffer checks that EOF allows submitted samples to play.
func TestDrainIncludesDeviceBuffer(t *testing.T) {
	o, render := newManualOutput(t)
	o.device.(*manualDevice).latency = 20 * time.Millisecond
	if err := o.Write(t.Context(), testPCM(1), 1, 0, 1); err != nil {
		t.Fatal(err)
	}
	render(make([]byte, 8))
	start := time.Now()
	if err := o.Drain(t.Context(), o.Written()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("drained before device buffer finished")
	}
}

func newManualOutput(t *testing.T) (*Output, func([]byte)) {
	t.Helper()
	var render func([]byte)
	o, err := NewOutput(Settings{Device: "auto", SampleRate: 48000, BufferMS: 50}, func(s Settings, callback func([]byte)) (Device, error) {
		render = callback
		return &manualDevice{settings: s}, nil
	}, 50, false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Close)
	return o, render
}
func testPCM(n int) []byte {
	data := make([]byte, n*8)
	for i := range n {
		binary.LittleEndian.PutUint32(data[i*8:], math.Float32bits(.8))
		binary.LittleEndian.PutUint32(data[i*8+4:], math.Float32bits(-.4))
	}
	return data
}

// TestOutputConsumptionPauseGainAndTap verifies consumption clocks and pre-volume analysis.
func TestOutputConsumptionPauseGainAndTap(t *testing.T) {
	o, render := newManualOutput(t)
	data := testPCM(480)
	if err := o.Write(t.Context(), data, 7, time.Second, float64(time.Second)/48000); err != nil {
		t.Fatal(err)
	}
	if _, pos := o.Position(); pos != 0 {
		t.Fatal("queued samples advanced position")
	}
	o.SetPaused(true)
	out := make([]byte, len(data))
	render(out)
	if _, pos := o.Position(); pos != 0 {
		t.Fatal("pause advanced position")
	}
	o.SetPaused(false)
	render(out)
	if got := math.Float32frombits(binary.LittleEndian.Uint32(out)); math.Abs(float64(got-.1)) > 1e-6 {
		t.Fatalf("gain: %v", got)
	}
	if id, pos := o.Position(); id != 7 || pos != 1010*time.Millisecond {
		t.Fatalf("position %d %s", id, pos)
	}
	if n := o.ReadAnalysis(out); n != len(data) {
		t.Fatalf("tap bytes %d", n)
	}
	if got := math.Float32frombits(binary.LittleEndian.Uint32(out)); got != .8 {
		t.Fatalf("tap should precede gain: %v", got)
	}
	o.SetMuted(true)
	o.Write(t.Context(), data, 7, 1010*time.Millisecond, float64(time.Second)/48000)
	render(out)
	for _, b := range out {
		if b != 0 {
			t.Fatal("muted output was audible")
		}
	}
	before := o.position.Load()
	render(out)
	if o.position.Load() != before {
		t.Fatal("underrun advanced source position")
	}
}

// TestOutputCancellationUnblocksProducerAndDrain verifies shutdown under backpressure.
func TestOutputCancellationUnblocksProducerAndDrain(t *testing.T) {
	o, _ := newManualOutput(t)
	if err := o.Write(t.Context(), testPCM(len(o.frames)), 1, 0, 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 2)
	go func() { done <- o.Write(ctx, testPCM(1), 1, 0, 1) }()
	go func() { done <- o.Drain(ctx, o.Written()) }()
	cancel()
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation hung")
		}
	}
}

// TestDeviceFailureKeepsWorkingOutput verifies transactional device selection.
func TestDeviceFailureKeepsWorkingOutput(t *testing.T) {
	var devices []*manualDevice
	o, err := NewOutput(Settings{Device: "auto", SampleRate: 48000, BufferMS: 100}, func(s Settings, _ func([]byte)) (Device, error) {
		if s.Device == "missing" {
			return nil, errors.New("disconnected")
		}
		d := &manualDevice{settings: s}
		devices = append(devices, d)
		return d, nil
	}, 50, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if err := o.SetDevice("missing"); err == nil {
		t.Fatal("missing output accepted")
	}
	if devices[0].closes != 0 || o.Info().ID != "auto" {
		t.Fatal("working output changed after failure")
	}
	if err := o.SetDevice("headphones"); err != nil {
		t.Fatal(err)
	}
	if devices[0].closes != 1 || o.Info().ID != "headphones" {
		t.Fatal("device switch did not commit")
	}
}

// BenchmarkOutputCallback measures the steady-state callback and sample transport.
func BenchmarkOutputCallback(b *testing.B) {
	o, err := NewOutput(Settings{SampleRate: 48000, BufferMS: 100}, func(s Settings, _ func([]byte)) (Device, error) { return &manualDevice{settings: s}, nil }, 100, false, false)
	if err != nil {
		b.Fatal(err)
	}
	defer o.Close()
	data := testPCM(480)
	dst := make([]byte, len(data))
	b.ReportAllocs()
	for b.Loop() {
		o.Write(b.Context(), data, 1, 0, 1)
		o.render(dst)
		o.ReadAnalysis(dst)
	}
}
