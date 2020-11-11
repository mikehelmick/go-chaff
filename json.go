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
	"net/http"
)

const (
	// Number of bytes added by the content type header for application/json
	contentHeaderSize = uint64(31)
)

// ProduceJSONFn is a function for producing JSON responses.
type ProduceJSONFn func(string) interface{}

// The default JSON object that is returned.
type BasicPadding struct {
	Padding string `json:"padding"`
}

// The default ProduceJSONFn
func PaddingWriterFn(randomData string) interface{} {
	return &BasicPadding{
		Padding: randomData,
	}
}

// JSONResponder implements the Responder interface and
// allows you to reply to chaff reqiests with a custom JSON object.
//
// To use a function
// that will be given the heuristically sized payload so you can transform
// it into the struct that you want to serialize.
type JSONResponder struct {
	fn ProduceJSONFn
}

// NewJSONResponse creates a new JSON responder
// Requres a ProduceJSONFn that will be given the random data payload
// and is responsible for putting it into a struct that can be marshalled
// as the JSON response.
func NewJSONResponder(fn ProduceJSONFn) Responder {
	return &JSONResponder{
		fn: fn,
	}
}

func DefaultJSONResponder() Responder {
	return &JSONResponder{
		fn: PaddingWriterFn,
	}
}

func (j *JSONResponder) Write(headerSize, bodySize uint64, w http.ResponseWriter, r *http.Request) error {
	var bodyData []byte
	var err error
	if bodySize > 0 {
		bodyData, err = json.Marshal(j.fn(RandomData(bodySize)))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, "{\"error\": \"%v\"}", err.Error())
			return err
		}
	}

	w.WriteHeader(http.StatusOK)
	// Generate the response details.
	if headerSize > contentHeaderSize {
		w.Header().Add(Header, RandomData(headerSize-contentHeaderSize-uint64(len(Header))))
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, "%s", bodyData)

	return nil
}
