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
	"fmt"
	"log"
	"net/http"
	"time"
)

// ProduceJSONFn is a function for producing JSON responses.
type ProduceJSONFn func(string) interface{}

// JSONResponse is an HTTP handler that can wrap a tracker and response with
// a custom JSON object.
//
// To use, you must provide an instanciated Tracker as well as a function
// that will be given the heuristically sized payload so you can transform
// it into the struct that you want to serialize.
type JSONResponse struct {
	t  *Tracker
	fn ProduceJSONFn
}

// NewJSONResponse creates a new JSON responder
// Requres a ProduceJSONFn that will be given the random data payload
// and is responsible for putting it into a struct that can be marshalled
// as the JSON response.
func NewJSONResponse(t *Tracker, fn ProduceJSONFn) *JSONResponse {
	return &JSONResponse{t, fn}
}

func (j *JSONResponse) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	details := j.t.CalculateProfile()

	var bodyData []byte
	var err error
	if details.bodySize > 0 {
		bodyData, err = json.Marshal(j.fn(randomData(details.bodySize)))
		if err != nil {
			log.Printf("error: unable to marshal chaff JSON response: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, "{\"error\": \"%v\"}", err.Error())
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	// Generate the response details.
	if details.headerSize > 0 {
		w.Header().Add(Header, randomData(details.headerSize))
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, "%s", bodyData)

	j.t.normalizeLatnecy(start, details.latencyMs)
}
