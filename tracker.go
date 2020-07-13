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
}

type request struct {
	latencyMs  int64
	bodySize   int
	headerSize int
}

func newRequest(start, end time.Time, headerSize, bodySize int) *request {
	return &request{
		latencyMs:  end.Sub(start).Milliseconds(),
		headerSize: headerSize,
		bodySize:   bodySize,
	}
}

// New creates a new tracker with the `DefaultCapacity`.
func New() *Tracker {
	t, _ := NewTracker(DefaultCapacity)
	return t
}

// NewTracker creates a tracker with custom capacity.
// Launches a goroutine to update the request metrics.
// To shut this down, use the .Close() method.
func NewTracker(cap int) (*Tracker, error) {
	if cap < 1 || cap > DefaultCapacity {
		return nil, fmt.Errorf("cap must be 1 <= cap <= 100, got: %v", cap)
	}

	t := &Tracker{
		buffer: make([]*request, 0, int(cap)),
		size:   0,
		cap:    cap,
		pos:    0,
		ch:     make(chan *request, cap),
		done:   make(chan struct{}),
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

	var latency, hSize, bSize int64
	for _, r := range t.buffer {
		latency += r.latencyMs
		hSize += int64(r.headerSize)
		bSize += int64(r.bodySize)
	}
	divisor := int64(t.size)
	return &request{
		latencyMs:  latency / divisor,
		headerSize: int(hSize / divisor),
		bodySize:   int(bSize / divisor),
	}
}

func randomData(size int) string {
	// Account for base64 overhead
	size = 3 * size / 4
	buffer := make([]byte, size)
	_, err := rand.Read(buffer)
	if err != nil {
		return http.StatusText(http.StatusInternalServerError)
	}
	return base64.StdEncoding.EncodeToString(buffer)
}

// ServerHTTP is the chaff request handler. Based on the current request profile
// the requst will be held for a certian period of time and then return
// approximate size random data.
func (t *Tracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	details := t.CalculateProfile()

	w.WriteHeader(http.StatusOK)
	// Generate the response details.
	if details.headerSize > 0 {
		w.Header().Add(Header, randomData(details.headerSize))
	}
	if details.bodySize > 0 {
		if _, err := w.Write([]byte(randomData(details.bodySize))); err != nil {
			log.Printf("chaff request failed to write: %v", err)
		}
	}

	// Normalize the latency.
	elapsed := time.Now().Sub(start)
	if rem := details.latencyMs - elapsed.Milliseconds(); rem > 0 {
		time.Sleep(time.Duration(rem) * time.Millisecond)
	}
}

// write through wraps an http.ResponseWriter so that we can count the
// number of bytes that are written by the delegate handler.
type writeThrough struct {
	size int
	w    http.ResponseWriter
}

func (wt *writeThrough) Header() http.Header {
	return wt.w.Header()
}

func (wt *writeThrough) Write(b []byte) (int, error) {
	wt.size += len(b)
	return wt.w.Write(b)
}

func (wt *writeThrough) WriteHeader(statusCode int) {
	wt.w.WriteHeader(statusCode)
}

// Track provides the necessary http middleware function.
func (t *Tracker) Track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		proxyWriter := &writeThrough{w: w}
		next.ServeHTTP(proxyWriter, r)
		end := time.Now()

		// grab the size of the headers that are present.
		headerSize := 0
		for k, vals := range w.Header() {
			headerSize += len(k)
			for _, v := range vals {
				headerSize += len(v)
			}
		}
		select {
		case t.ch <- newRequest(start, end, headerSize, proxyWriter.size):
		default: // channel full, drop request.
		}
	})
}
