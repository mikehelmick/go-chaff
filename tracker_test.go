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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestRandomData(t *testing.T) {
	t.Parallel()

	d := RandomData(0)
	if d != "" {
		t.Fatalf("expected empty string, got: %q", d)
	}

	d = RandomData(MaxRandomBytes * 2)
	b, err := base64.StdEncoding.DecodeString(d)
	if err != nil {
		t.Fatal(err)
	}
	if l := len(b); l < int(float32(MaxRandomBytes)*0.99) || l > int(float32(MaxRandomBytes)*1.01) {
		t.Fatalf("length is outside of 1pct of expected, want: %d got: %d", MaxRandomBytes, l)
	}
}

func checkLength(t *testing.T, expected int, length int) {
	t.Helper()
	lower := float64(expected) * 0.99
	upper := float64(expected) * 1.01

	if l := float64(length); l < lower || l > upper {
		t.Errorf("genrated data not within 1%% of %v, %v - %v, got %v", expected, lower, upper, l)
	}
}

func TestChaff(t *testing.T) {
	t.Parallel()
	track := New()
	defer track.Close()

	// Seed the tracker with a single request.
	track.recordRequest(&request{25, 250, 100})

	w := httptest.NewRecorder()
	r, err := http.NewRequest("GET", "/", strings.NewReader(""))
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}

	before := time.Now()
	track.ServeHTTP(w, r)
	after := time.Now()

	if d := after.Sub(before); d < 25*time.Millisecond {
		t.Errorf("not enough time passed, want >= 25ms, got: %v", d)
	}

	if w.Code != http.StatusOK {
		t.Errorf("wrong code, want: %v, got: %v", http.StatusOK, w.Code)
	}

	if header := w.Header().Get(Header); header == "" {
		t.Errorf("expected header '%v' missing", Header)
	} else {
		checkLength(t, 100, len(header))
	}
	checkLength(t, 250, len(w.Body.Bytes()))
}

func TestTracking(t *testing.T) {
	t.Parallel()
	track := New()
	defer track.Close()

	{
		want := &request{}
		got := track.CalculateProfile()
		if diff := cmp.Diff(want, got, cmp.AllowUnexported(request{})); diff != "" {
			t.Errorf("mismatch (-want, +got):\n%s", diff)
		}
	}

	for i := 0; i <= DefaultCapacity*2; i++ {
		wrapped := track.Track(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(1 * time.Millisecond)
				w.WriteHeader(http.StatusAccepted)
				w.Header().Add("padding", strings.Repeat("a", i+1))
				fmt.Fprintf(w, "%s", strings.Repeat("b", i+1))
			}))

		recorder := httptest.NewRecorder()
		request, err := http.NewRequest("GET", "/", strings.NewReader(""))
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}

		wrapped.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("wrong error code: want: %v, got: %v", http.StatusAccepted, recorder.Code)
		}
	}

	got := track.CalculateProfile()
	// requests are fast enough that 1ms is reasonable.
	// sum(101:200)/100 -> 150
	// for header there is an extra 7 bytes for header name
	want := &request{1, 150, 157}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(request{})); diff != "" {
		t.Errorf("mismatch (-want, +got):\n%s", diff)
	}
}

func TestMax(t *testing.T) {
	t.Parallel()

	track := New(WithMaxLatency(25))
	defer track.Close()

	var wg sync.WaitGroup
	for i := 0; i <= DefaultCapacity*2; i++ {
		wrapped := track.Track(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(50 * time.Millisecond)
				w.WriteHeader(http.StatusAccepted)
				w.Header().Add("padding", strings.Repeat("a", i+1))
				fmt.Fprintf(w, "%s", strings.Repeat("b", i+1))
			}))

		recorder := httptest.NewRecorder()
		request, err := http.NewRequest("GET", "/", strings.NewReader(""))
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}

		wg.Add(1)
		go func(t *testing.T) {
			defer wg.Done()
			t.Helper()
			wrapped.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusAccepted {
				t.Errorf("wrong error code: want: %v, got: %v", http.StatusAccepted, recorder.Code)
			}
		}(t)
	}

	wg.Wait()

	got := track.CalculateProfile()
	// Only checking latency
	wantLatency := uint64(25)
	if diff := cmp.Diff(wantLatency, got.latencyMs); diff != "" {
		t.Errorf("mismatch (-want, +got):\n%s", diff)
	}
}

// TestChaffHeaderDelivered exercises the chaff handler through a real HTTP
// server (rather than httptest.ResponseRecorder, which mutates the header map
// in place even after WriteHeader). This ensures the chaff header is actually
// flushed to the client, which is essential for chaff responses to be
// indistinguishable from real traffic.
func TestChaffHeaderDelivered(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		resp        Responder
		contentType string
	}{
		{"plain", &PlainResponder{}, ""},
		{"json", DefaultJSONResponder(), "application/json"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			track, err := NewTracker(tc.resp, DefaultCapacity)
			if err != nil {
				t.Fatalf("NewTracker: %v", err)
			}
			defer track.Close()

			// Seed with a request that has non-trivial header and body sizes.
			track.recordRequest(&request{1, 250, 100})

			srv := httptest.NewServer(track.HandleChaff())
			defer srv.Close()

			resp, err := http.Get(srv.URL)
			if err != nil {
				t.Fatalf("http.Get: %v", err)
			}
			defer resp.Body.Close()

			if got := resp.Header.Get(Header); got == "" {
				t.Errorf("chaff header %q was not delivered to the client", Header)
			}
			if tc.contentType != "" {
				if got := resp.Header.Get("Content-Type"); got != tc.contentType {
					t.Errorf("Content-Type = %q, want %q", got, tc.contentType)
				}
			}
		})
	}
}

// TestBodyCompressionEstimate verifies that, with WithBodyCompression enabled,
// the recorded body size reflects the compressed size of a compressible
// response rather than its raw size, while the raw byte count is still
// observed.
func TestBodyCompressionEstimate(t *testing.T) {
	t.Parallel()

	track, err := NewTracker(&PlainResponder{}, DefaultCapacity, WithBodyCompression(gzip.BestCompression))
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	defer track.Close()

	// Highly compressible body.
	body := []byte(strings.Repeat("a", 4096))

	wt := track.newWriteThrough(httptest.NewRecorder())
	if _, err := wt.Write(body); err != nil {
		t.Fatalf("Write: %v", err)
	}
	recorded := wt.finalize()

	if recorded == 0 || recorded >= uint64(len(body)) {
		t.Errorf("recorded body size = %d, want compressed size in (0, %d)", recorded, len(body))
	}
	if raw := atomic.LoadUint64(&wt.size); raw != uint64(len(body)) {
		t.Errorf("raw size = %d, want %d", raw, len(body))
	}
}

// TestBodyCompressionIncompressible verifies that for incompressible data the
// recorded size never exceeds the raw size (a real compressor would send the
// payload uncompressed rather than enlarge it).
func TestBodyCompressionIncompressible(t *testing.T) {
	t.Parallel()

	track, err := NewTracker(&PlainResponder{}, DefaultCapacity, WithBodyCompression(gzip.BestSpeed))
	if err != nil {
		t.Fatalf("NewTracker: %v", err)
	}
	defer track.Close()

	// RandomData is high-entropy and will not compress.
	body := []byte(RandomData(4096))

	wt := track.newWriteThrough(httptest.NewRecorder())
	if _, err := wt.Write(body); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if recorded := wt.finalize(); recorded > uint64(len(body)) {
		t.Errorf("recorded body size = %d, want <= raw size %d", recorded, len(body))
	}
}

func TestWithBodyCompressionInvalidLevel(t *testing.T) {
	t.Parallel()

	if _, err := NewTracker(&PlainResponder{}, DefaultCapacity, WithBodyCompression(99)); err == nil {
		t.Error("expected error for invalid compression level, got nil")
	}
}

func TestJSONMiddleware(t *testing.T) {
	t.Parallel()
	type result struct {
		Name string `json:"name"`
	}
	jsonCount, nonJSONCount := 0, 0
	write := func(s string) interface{} {
		jsonCount += 1
		d, _ := json.Marshal(result{s})
		t.Logf("writing json: %v %v", result{s}, d)
		return result{s}
	}
	tracker, err := NewTracker(NewJSONResponder(write), DefaultCapacity)
	if err != nil {
		t.Fatalf("error creating tracker: %v", err)
	}
	defer tracker.Close()

	// Start the server
	srv := httptest.NewServer(tracker.HandleTrack(HeaderDetector("X-Chaff"),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nonJSONCount += 1
			w.Write([]byte("HERE"))
		})))
	defer srv.Close()

	// Send a non-chaff request.
	t.Logf("Getting non-chaff")
	if _, err := http.Get(srv.URL); err != nil {
		t.Fatalf("error connecting to server %v", err)
	} else if nonJSONCount != 1 {
		t.Errorf("nonJSONCount = %d, expected 1", nonJSONCount)
	} else if jsonCount != 0 {
		t.Errorf("jsonCount = %d, expected 0", jsonCount)
	}
	nonJSONCount, jsonCount = 0, 0

	// Send a chaff request
	req, err := http.NewRequest("GET", srv.URL, nil)
	if err != nil {
		t.Fatalf("error creating request %v", err)
	}
	req.Header.Add("X-Chaff", "true")
	client := http.Client{}
	t.Logf("Getting chaff")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("error getting chaff: %v", err)
	} else if jsonCount != 1 {
		t.Errorf("jsonCount = %d, expected 1", jsonCount)
	} else if nonJSONCount != 0 {
		t.Errorf("nonJSONCount = %d, expected 0", nonJSONCount)
	}
	t.Logf("%v", resp.Header)
	defer resp.Body.Close()
	dat, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("error reading response: %v", err)
	}
	t.Logf("%s", string(dat))
}
