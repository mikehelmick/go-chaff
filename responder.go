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

import "net/http"

// Responder allows you to extend the chaff library with custom
// responders.
type Responder interface {
	// Writes the appropriately sized header and body in the desired format.
	Write(headerSize, bodySize uint64, w http.ResponseWriter, r *http.Request) error
}
