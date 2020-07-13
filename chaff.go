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

// Package chaff provides useful primitives for enabling chaff requests on your
// http servers.
//
// One might want to employ chaff requests to obscure which clients are and are
// not communicating with your server by allowing all clients to periodically
// communicate with the server.
//
// Request size and latencies are tracked on a rolling basis so that responses
// to chaff requests are similar to real traffic in terms of latency and
// response size.
package chaff
