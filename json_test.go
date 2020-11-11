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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type Example struct {
	Field string `json:"field"`
}

func produceExample(data string) interface{} {
	return &Example{Field: data}
}

func TestJSONChaff(t *testing.T) {
	track := New()
	defer track.Close()

	// Seed the tracker with a single request.
	track.recordRequest(&request{25, 250, 100})

	w := httptest.NewRecorder()
	r, err := http.NewRequest("GET", "/", strings.NewReader(""))
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}

	responder := NewJSONResponder(produceExample)

	before := time.Now()
	track.ChaffHandler(responder).ServeHTTP(w, r)
	after := time.Now()

	if d := after.Sub(before); d < 25*time.Millisecond {
		t.Errorf("not enough time passed, want >= 25ms, got: %v", d)
	}

	if w.Code != http.StatusOK {
		t.Errorf("wrong code, want: %v, got: %v", http.StatusOK, w.Code)
	}

	headerSize := 0
	for k, v := range w.Header() {
		headerSize += len(k)
		if len(v) > 0 {
			headerSize += len(v[0])
		}
	}
	checkLength(t, 100, headerSize)

	var response Example
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("unable to read json response: %v", err)
	}
	checkLength(t, 250, len(response.Field))
}
