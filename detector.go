// Copyright 2020 Mike Helmick
// Copyright 2020 Seth Vargo
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

import "net/http"

type Detector interface {
	IsChaff(r *http.Request) bool
}

var _ Detector = (DetectorFunc)(nil)

type DetectorFunc func(r *http.Request) bool

func (d DetectorFunc) IsChaff(r *http.Request) bool {
	return d(r)
}

// HeaderDetector is a detector that searches for the header's presence to mark
// a request as chaff.
func HeaderDetector(h string) Detector {
	return DetectorFunc(func(r *http.Request) bool {
		return r.Header.Get(h) != ""
	})
}
