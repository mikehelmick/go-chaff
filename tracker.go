// Copyright 2020 Mike Helmick
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package chaff

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Header          = "X-Chaff"
	DefaultCapacity = 100
	MaxRandomBytes  = 1000000
)

// Tracker represents the status of a latency and request size tracker.
// It contains middleware that can be injected to automate keeping a rolling
// history of requests.
//
// It also implements http.Handler and can be used to server the chaff request
// handler.
//
// Response details are sent through a buffered channel. If the channel is full
// (i.e. this library is falling behind or requests volumes are too large),
// then some individual requests will be dropped.
type Tracker struct {
	mu           sync.RWMutex
	buffer       []*request
	size         int
	cap          int
	pos          int
	ch           chan *request
	done         chan struct{}
	resp         Responder
	maxLatencyMs uint64
	closeOnce    sync.Once

	// estimateCompression, when true, causes the tracker to record the
	// estimated compressed size of real response bodies instead of their raw
	// size. gzipPool holds reusable gzip writers configured at gzipLevel.
	estimateCompression bool
	gzipLevel           int
	gzipPool            sync.Pool
}

type request struct {
	latencyMs  uint64
	bodySize   uint64
	headerSize uint64
}

func newRequest(start, end time.Time, headerSize, bodySize uint64) *request {
	return &request{
		latencyMs:  uint64(end.Sub(start).Milliseconds()),
		headerSize: headerSize,
		bodySize:   bodySize,
	}
}

// New creates a new tracker with the `DefaultCapacity`.
func New(opts ...Option) *Tracker {
	t, _ := NewTracker(&PlainResponder{}, DefaultCapacity, opts...)
	return t
}

// Option defines a method for applying options when configuring a new tracker.
type Option func(*Tracker)

// WithMaxLatency puts a cap on the tunnel latency.
func WithMaxLatency(maxLatencyMs uint64) Option {
	return func(t *Tracker) {
		t.maxLatencyMs = maxLatencyMs
	}
}

// WithBodyCompression makes the tracker record the estimated *compressed* size
// of real response bodies instead of their raw size, using gzip at the given
// level (one of the compress/gzip level constants).
//
// This is useful when responses are compressed by a component the tracker
// cannot observe directly, such as a reverse proxy or CDN. In that case the raw
// body size overstates what an observer sees on the wire, and because chaff
// payloads are high-entropy (incompressible) data, chaff responses would be
// conspicuously larger than real, compressible responses. Sizing chaff to the
// estimated compressed size keeps the two indistinguishable on the wire.
//
// The estimation runs while the wrapped handler executes, so its CPU cost is
// included in the tracked request latency and is therefore replayed for chaff
// responses. It adds CPU overhead to every tracked request, so it is opt-in.
// The estimate is only as good as the configured level matches the downstream
// compressor; header compression (e.g. HTTP/2 HPACK) is not modeled.
//
// NewTracker returns an error if level is not a valid gzip level.
func WithBodyCompression(level int) Option {
	return func(t *Tracker) {
		t.estimateCompression = true
		t.gzipLevel = level
	}
}

// NewTracker creates a tracker with custom capacity.
// Launches a goroutine to update the request metrics.
// To shut this down, use the .Close() method.
// The Responder parameter is used to write the output. If non is specified,
// the tracker will default to the "PlainResponder" which just writes the raw
// chaff bytes.
func NewTracker(resp Responder, cap int, opts ...Option) (*Tracker, error) {
	if cap < 1 || cap > DefaultCapacity {
		return nil, fmt.Errorf("cap must be 1 <= cap <= 100, got: %v", cap)
	}

	if resp == nil {
		return nil, fmt.Errorf("responder must be non-nil")
	}

	t := &Tracker{
		buffer:       make([]*request, 0, int(cap)),
		size:         0,
		cap:          cap,
		pos:          0,
		ch:           make(chan *request, cap),
		done:         make(chan struct{}),
		resp:         resp,
		maxLatencyMs: 0,
	}

	// Apply options.
	for _, opt := range opts {
		opt(t)
	}

	if t.estimateCompression {
		// Validate the level once up front so the pool factory below can safely
		// ignore the error.
		if _, err := gzip.NewWriterLevel(io.Discard, t.gzipLevel); err != nil {
			return nil, fmt.Errorf("invalid compression level: %w", err)
		}
		level := t.gzipLevel
		t.gzipPool.New = func() interface{} {
			gz, _ := gzip.NewWriterLevel(io.Discard, level)
			return gz
		}
	}

	go t.updater()
	return t, nil
}

// recordRequest actually puts a request in the circular buffer.
func (t *Tracker) recordRequest(record *request) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.size < t.cap {
		t.buffer = append(t.buffer, record)
		t.size++
		return
	}
	// Working as a circular buffer, just overrite and move on.
	t.buffer[t.pos] = record
	t.pos = (t.pos + 1) % t.cap
}

// updater is the go routine that is launched to pull requst details from
// the request channel.
func (t *Tracker) updater() {
	for {
		select {
		case record := <-t.ch:
			t.recordRequest(record)
		case <-t.done:
			return
		}
	}
}

// Close stops the updating goroutine. It is safe to call Close multiple times
// and safe to call concurrently with in-flight tracked requests.
//
// The request channel is intentionally not closed: request handlers are the
// senders, and closing it from here could cause a "send on closed channel"
// panic for a request that is still being recorded. Closing the done channel
// is sufficient to terminate the updater goroutine.
func (t *Tracker) Close() {
	t.closeOnce.Do(func() {
		close(t.done)
	})
}

// CalculateProfile takes a read lock over the source data and
// returns the current average latency and request sizes.
func (t *Tracker) CalculateProfile() *request {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.size == 0 {
		return &request{}
	}

	var latency, hSize, bSize uint64
	for _, r := range t.buffer {
		latency += r.latencyMs
		hSize += uint64(r.headerSize)
		bSize += uint64(r.bodySize)
	}
	divisor := uint64(t.size)

	latencyMs := latency / divisor
	if max := t.maxLatencyMs; max > 0 && latencyMs > max {
		latencyMs = max
	}

	return &request{
		latencyMs:  latencyMs,
		headerSize: uint64(hSize / divisor),
		bodySize:   uint64(bSize / divisor),
	}
}

// RandomData generates size bytes of random base64 data.
func RandomData(size uint64) string {
	// Account for base64 overhead
	size = 3 * size / 4
	if size <= 0 {
		return ""
	}
	if size > MaxRandomBytes {
		size = MaxRandomBytes
	}

	buffer := make([]byte, size)
	_, err := rand.Read(buffer)
	if err != nil {
		return http.StatusText(http.StatusInternalServerError)
	}
	return base64.StdEncoding.EncodeToString(buffer)
}

// ServeHTTP implements http.Handler. See HandleChaff for more details.
func (t *Tracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t.HandleChaff().ServeHTTP(w, r)
}

func (t *Tracker) ChaffHandler(responder Responder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		details := t.CalculateProfile()

		if err := responder.Write(details.headerSize, details.bodySize, w, r); err != nil {
			log.Printf("error writing chaff response: %v", err)
		}

		t.normalizeLatnecy(start, details.latencyMs)
	})
}

// HandleChaff is the chaff request handler. Based on the current request
// profile the requst will be held for a certian period of time and then return
// approximate size random data.
func (t *Tracker) HandleChaff() http.Handler {
	return t.ChaffHandler(t.resp)
}

// Track wraps a http handler and collects metrics about the request for
// replaying later during a chaff response. It's suitable for use as a
// middleware function in common Go web frameworks.
func (t *Tracker) Track(next http.Handler) http.Handler {
	return t.HandleTrack(nil, next)
}

// HandleTrack wraps the given http handler and detector. If the request is
// deemed to be chaff (as determined by the Detector), the system sends a chaff
// response. Otherwise it returns the real response and adds it to the tracker.
func (t *Tracker) HandleTrack(d Detector, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d != nil && d.IsChaff(r) {
			// Send chaff response
			t.HandleChaff().ServeHTTP(w, r)
			return
		}

		// Handle the real request, gathering metadata
		start := time.Now()
		proxyWriter := t.newWriteThrough(w)
		next.ServeHTTP(proxyWriter, r)
		// finalize before stamping end so any compression-estimation cost
		// (including the final gzip flush) is part of the recorded latency.
		bodySize := proxyWriter.finalize()
		end := time.Now()

		// Grab the size of the headers that are present.
		var headerSize uint64
		for k, vals := range w.Header() {
			headerSize += uint64(len(k))
			for _, v := range vals {
				headerSize += uint64(len(v))
			}
		}

		// Save metadata
		select {
		case t.ch <- newRequest(start, end, headerSize, bodySize):
		default: // channel full, drop request.
		}
	})
}

func (t *Tracker) normalizeLatnecy(start time.Time, targetMs uint64) {
	elapsedMs := uint64(time.Since(start).Milliseconds())
	// If the response already took longer than the target latency there is
	// nothing to do. Guard against unsigned integer underflow before computing
	// the remaining sleep duration.
	if elapsedMs >= targetMs {
		return
	}
	time.Sleep(time.Duration(targetMs-elapsedMs) * time.Millisecond)
}

// countWriter is an io.Writer that only counts the bytes written to it. It is
// used as the sink for the compression estimator.
type countWriter struct {
	n uint64
}

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += uint64(len(p))
	return len(p), nil
}

// write through wraps an http.ResponseWriter so that we can count the number of
// bytes that are written by the delegate handler.
//
// When the owning tracker has compression estimation enabled, writes are also
// teed through gz (whose output lands in counter) so finalize can report the
// estimated compressed body size.
type writeThrough struct {
	size    uint64
	w       http.ResponseWriter
	t       *Tracker
	gz      *gzip.Writer
	counter *countWriter
}

// newWriteThrough builds a writeThrough for w, wiring up the compression
// estimator if the tracker is configured for it.
func (t *Tracker) newWriteThrough(w http.ResponseWriter) *writeThrough {
	wt := &writeThrough{w: w, t: t}
	if t.estimateCompression {
		wt.counter = &countWriter{}
		wt.gz = t.gzipPool.Get().(*gzip.Writer)
		wt.gz.Reset(wt.counter)
	}
	return wt
}

func (wt *writeThrough) Header() http.Header {
	return wt.w.Header()
}

func (wt *writeThrough) Write(b []byte) (int, error) {
	atomic.AddUint64(&wt.size, uint64(len(b)))
	if wt.gz != nil {
		// Best-effort estimate only: an error here affects the size estimate,
		// not the response that is actually sent, so it is intentionally
		// ignored.
		_, _ = wt.gz.Write(b)
	}
	return wt.w.Write(b)
}

// finalize flushes the compression estimator (if any) and returns the body
// size to record. When compression is estimated, it returns the smaller of the
// raw and compressed sizes (a real compressor never enlarges a payload),
// otherwise it returns the raw byte count.
//
// It must be called before the request latency is stamped so that the final
// gzip flush is attributed to request latency.
func (wt *writeThrough) finalize() uint64 {
	raw := atomic.LoadUint64(&wt.size)
	if wt.gz == nil {
		return raw
	}
	_ = wt.gz.Close()
	compressed := wt.counter.n
	wt.t.gzipPool.Put(wt.gz)
	wt.gz = nil
	if compressed < raw {
		return compressed
	}
	return raw
}

func (wt *writeThrough) WriteHeader(statusCode int) {
	wt.w.WriteHeader(statusCode)
}
