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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func handler(w http.ResponseWriter, r *http.Request) {

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

	if header, ok := w.HeaderMap[Header]; !ok {
		t.Errorf("expected header '%v' missing", Header)
	} else {
		checkLength(t, 100, len(header[0]))
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
				w.Write([]byte(strings.Repeat("b", i+1)))
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
