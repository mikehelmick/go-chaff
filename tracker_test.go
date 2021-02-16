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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestRandomData(t *testing.T) {
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

func TestJSONMiddleware(t *testing.T) {
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
	t.Logf(string(dat))
}
