package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/willibrandon/chill/internal/playback"
)

// TestPlaybackBuffersNetworkSources verifies routing through real native
// decoding, short-file completion, and unchanged local-file PCM.
func TestPlaybackBuffersNetworkSources(t *testing.T) {
	data, err := os.ReadFile("testdata/audio/tone.wav")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		w.Write(data)
	}))
	defer server.Close()
	var want []byte
	for _, source := range []string{"testdata/audio/tone.wav", server.URL} {
		r, _, err := openPlaybackPCM(t.Context(), source, 0, false, defaultAudioSettings(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, buffered := r.(*bufferedPCM)
		if buffered != (source == server.URL) {
			t.Fatalf("unexpected buffering for %q", source)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if want == nil {
			want = got
		} else if !bytes.Equal(got, want) {
			t.Fatal("network buffering changed decoded samples")
		}
	}
}

// TestBufferedPCMNativeCancellation interrupts a decoder blocked on HTTP while
// unread PCM remains in the reserve, without retaining the network worker.
func TestBufferedPCMNativeCancellation(t *testing.T) {
	data, err := os.ReadFile(stereoFixture(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		defer close(closed)
		w.Write(data[:44+48000*4*2])
		w.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	r, _, err := openPlaybackPCM(t.Context(), server.URL, 0, false, defaultAudioSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := io.ReadFull(r, make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("buffered native decoder did not stop")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP stream remained open")
	}
}

// TestBufferedPCMOrderAcrossWraps guards sample order and partial reads at both
// ends of the circular reserve.
func TestBufferedPCMOrderAcrossWraps(t *testing.T) {
	for _, capacity := range []int{8, 31, 1024} {
		synctest.Test(t, func(t *testing.T) {
			want := make([]byte, 10003)
			for i := range want {
				want[i] = byte(i*17 + i/256)
			}
			r, _, err := newBufferedPCM(t.Context(), capacity, capacity/2, nil, func(context.Context, func(string)) (io.ReadCloser, string, error) {
				return io.NopCloser(bytes.NewReader(want)), "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			var got bytes.Buffer
			var block [13]byte
			for {
				n, err := r.Read(block[:])
				got.Write(block[:n])
				if err != nil {
					if !errors.Is(err, io.EOF) {
						t.Fatal(err)
					}
					break
				}
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("PCM order changed with capacity %d", capacity)
			}
		})
	}
}

type burstPCM struct {
	ctx                      context.Context
	position, size, boundary int
	delay                    time.Duration
}

// Read simulates a decoder whose next network fetch stalls at a sample boundary.
func (r *burstPCM) Read(dst []byte) (int, error) {
	if r.position == r.size {
		return 0, io.EOF
	}
	if r.position == r.boundary {
		select {
		case <-time.After(r.delay):
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
	n := min(len(dst), r.size-r.position)
	if r.position < r.boundary {
		n = min(n, r.boundary-r.position)
	}
	for i := range n / 4 {
		binary.LittleEndian.PutUint32(dst[i*4:], math.Float32bits(.25))
	}
	r.position += n
	return n, nil
}

// Close has no external resources to release.
func (*burstPCM) Close() error { return nil }

// TestNetworkReadAheadAbsorbsDecoderStalls reproduces a network read that the
// device-paced decoder cannot begin early enough, then verifies continuous PCM.
func TestNetworkReadAheadAbsorbsDecoderStalls(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "device-only", true: "read-ahead"}[buffered], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const rate = 48000
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var source io.ReadCloser = &burstPCM{ctx: ctx, size: rate * 8 * 40, boundary: rate * 8 * 20, delay: 12400 * time.Millisecond}
				if buffered {
					r, _, err := newBufferedPCM(ctx, rate*8*30, rate*8, nil, func(context.Context, func(string)) (io.ReadCloser, string, error) { return source, "", nil })
					if err != nil {
						t.Fatal(err)
					}
					source = r
				}
				defer source.Close()
				o, err := playback.NewOutput(playback.Settings{SampleRate: rate, BufferMS: 100}, testDeviceFactory, 100, false, false)
				if err != nil {
					t.Fatal(err)
				}
				defer o.Close()
				var wg sync.WaitGroup
				wg.Go(func() {
					var block [512 * 8]byte
					var position time.Duration
					for {
						n, err := source.Read(block[:])
						if n > 0 {
							if err := o.Write(ctx, block[:n], 1, position, float64(time.Second)/rate); err != nil {
								return
							}
							position += time.Duration(n/8) * time.Second / rate
						}
						if err != nil {
							return
						}
					}
				})
				time.Sleep(time.Second)
				before := o.Underruns()
				time.Sleep(35 * time.Second)
				after := o.Underruns()
				if buffered && after != before {
					t.Fatalf("network delay leaked into output: %d underruns", after-before)
				}
				if !buffered && after == before {
					t.Fatal("control did not reproduce the missing read-ahead")
				}
				cancel()
				wg.Wait()
			})
		})
	}
}

// TestBufferedPCMShortStreamAndError preserves every byte before EOF or failure,
// including streams shorter than the initial buffering threshold.
func TestBufferedPCMShortStreamAndError(t *testing.T) {
	for _, end := range []error{io.EOF, errors.New("upstream failed")} {
		synctest.Test(t, func(t *testing.T) {
			want := bytes.Repeat([]byte("samples!"), 31)
			r, _, err := newBufferedPCM(t.Context(), 4096, 1024, nil, func(context.Context, func(string)) (io.ReadCloser, string, error) {
				return io.NopCloser(io.MultiReader(bytes.NewReader(want), errorReader{end})), "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			got, err := io.ReadAll(r)
			if !bytes.Equal(got, want) {
				t.Fatal("buffer truncated PCM")
			}
			if errors.Is(end, io.EOF) {
				end = nil
			}
			if !errors.Is(err, end) {
				t.Fatalf("error = %v, want %v", err, end)
			}
		})
	}
}

type errorReader struct{ err error }

// Read terminates a fixture with its requested diagnostic.
func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

// TestBufferedPCMTitlesFollowConsumption keeps prefetched stream tags from
// changing now-playing before the associated PCM leaves the network reserve.
func TestBufferedPCMTitlesFollowConsumption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got []string
		r, _, err := newBufferedPCM(t.Context(), 4096, 8, func(s string) { got = append(got, s) }, func(ctx context.Context, title func(string)) (io.ReadCloser, string, error) {
			pr, pw := io.Pipe()
			go func() {
				defer pw.Close()
				title("first")
				pw.Write(bytes.Repeat([]byte{1}, 1024))
				// Let the producer commit the first samples before the next tag.
				time.Sleep(time.Millisecond)
				title("second")
				pw.Write(bytes.Repeat([]byte{2}, 1024))
			}()
			return pr, "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		time.Sleep(10 * time.Millisecond)
		if len(got) != 0 {
			t.Fatalf("prefetch announced titles: %v", got)
		}
		buf := make([]byte, 1024)
		if _, err := io.ReadFull(r, buf); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != "first" {
			t.Fatalf("first audio tags: %v", got)
		}
		if _, err := io.ReadFull(r, buf); err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[1] != "second" {
			t.Fatalf("second audio tags: %v", got)
		}
	})
}

// TestBufferedPCMCancelUnblocksReadsAndFullReserve checks both shutdown paths.
func TestBufferedPCMCancelUnblocksReadsAndFullReserve(t *testing.T) {
	for _, full := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			r, _, err := newBufferedPCM(t.Context(), 1024, 512, nil, func(ctx context.Context, _ func(string)) (io.ReadCloser, string, error) {
				if full {
					return io.NopCloser(bytes.NewReader(make([]byte, 4096))), "", nil
				}
				pr, pw := io.Pipe()
				context.AfterFunc(ctx, func() { pw.CloseWithError(ctx.Err()) })
				return pr, "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if full {
				r.mu.Lock()
				if r.written-r.read != 1024 {
					t.Error("reserve not bounded by capacity")
				}
				r.mu.Unlock()
			}
			r.Close()
			if _, err := r.Read(make([]byte, 8)); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled read: %v", err)
			}
		})
	}
}

// TestBufferedPCMRefillsBeforeResuming prevents a stalled stream from repeatedly
// playing small fragments as individual network reads arrive.
func TestBufferedPCMRefillsBeforeResuming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var writer *io.PipeWriter
		r, _, err := newBufferedPCM(t.Context(), 64, 16, nil, func(ctx context.Context, _ func(string)) (io.ReadCloser, string, error) {
			reader, pw := io.Pipe()
			writer = pw
			context.AfterFunc(ctx, func() { pw.CloseWithError(ctx.Err()) })
			return reader, "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		writer.Write(make([]byte, 16))
		if _, err := io.ReadFull(r, make([]byte, 16)); err != nil {
			t.Fatal(err)
		}
		read := make(chan int, 1)
		go func() { n, _ := r.Read(make([]byte, 32)); read <- n }()
		synctest.Wait()
		writer.Write(make([]byte, 8))
		synctest.Wait()
		select {
		case n := <-read:
			t.Fatalf("resumed with only %d bytes after starvation", n)
		default:
		}
		writer.Write(make([]byte, 24))
		if n := <-read; n != 32 {
			t.Fatalf("refilled read = %d", n)
		}
	})
}
