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
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Header          = "X-Chaff"
	DefaultCapacity = 100
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
	mu     sync.RWMutex
	buffer []*request
	size   int
	cap    int
	pos    int
	ch     chan *request
	done   chan struct{}
	resp   Responder
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
func New() *Tracker {
	t, _ := NewTracker(&PlainResponder{}, DefaultCapacity)
	return t
}

// NewTracker creates a tracker with custom capacity.
// Launches a goroutine to update the request metrics.
// To shut this down, use the .Close() method.
// The Responder parameter is used to write the output. If non is specified,
// the tracker will default to the "PlainResponder" which just writes the raw
// chaff bytes.
func NewTracker(resp Responder, cap int) (*Tracker, error) {
	if cap < 1 || cap > DefaultCapacity {
		return nil, fmt.Errorf("cap must be 1 <= cap <= 100, got: %v", cap)
	}

	if resp == nil {
		return nil, fmt.Errorf("responder must be non-nil")
	}

	t := &Tracker{
		buffer: make([]*request, 0, int(cap)),
		size:   0,
		cap:    cap,
		pos:    0,
		ch:     make(chan *request, cap),
		done:   make(chan struct{}),
		resp:   resp,
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

// Close will stop the updating goroutine and closes all channels.
func (t *Tracker) Close() {
	t.done <- struct{}{}
	close(t.ch)
	close(t.done)
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

	return &request{
		latencyMs:  latency / divisor,
		headerSize: uint64(hSize / divisor),
		bodySize:   uint64(bSize / divisor),
	}
}

func RandomData(size uint64) string {
	// Account for base64 overhead
	size = 3 * size / 4
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
		proxyWriter := &writeThrough{w: w}
		next.ServeHTTP(proxyWriter, r)
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
		case t.ch <- newRequest(start, end, headerSize, proxyWriter.Size()):
		default: // channel full, drop request.
		}
	})
}

func (t *Tracker) normalizeLatnecy(start time.Time, targetMs uint64) {
	elapsed := time.Since(start)
	if rem := targetMs - uint64(elapsed.Milliseconds()); rem > 0 {
		time.Sleep(time.Duration(rem) * time.Millisecond)
	}
}

// write through wraps an http.ResponseWriter so that we can count the number of
// bytes that are written by the delegate handler.
type writeThrough struct {
	size uint64
	w    http.ResponseWriter
}

func (wt *writeThrough) Header() http.Header {
	return wt.w.Header()
}

func (wt *writeThrough) Write(b []byte) (int, error) {
	atomic.AddUint64(&wt.size, uint64(len(b)))
	return wt.w.Write(b)
}

func (wt *writeThrough) WriteHeader(statusCode int) {
	wt.w.WriteHeader(statusCode)
}

func (wt *writeThrough) Size() uint64 {
	return atomic.LoadUint64(&wt.size)
}
