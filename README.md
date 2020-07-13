# Go Chaff Tracker / Generator

[![GoDoc](https://img.shields.io/badge/go-documentation-blue.svg?style=flat-square)](https://pkg.go.dev/github.com/mikehelmick/go-chaff)
![Go](https://github.com/mikehelmick/go-chaff/workflows/Go/badge.svg?event=push)

This package provides the necessary tools to allow for your server to handle
chaff requests from clients. This technique can be used when you want to guard
against the fact that clients are connecting to your server is meaningful.

Use of this method allows your clients to periodically connect to the server
sending chaff instead of real requests.

There are two components, the middleware function that implements the tracking
and an http.Handler that serves the chaff requests.

## Usage

1. Create the tracker and install the middleware on routes you want to
   simulate the request latency and response size of.

```golang
r := mux.NewRouter()
track := chaff.New()
defer track.Close()
{
  sub := r.PathPrefix("").Subrouter()
  sub.Use(track.Track)
  sub.Handle(...) // your actual methods to simulate
}
```

2. Install a handler to handle the chaff requests.

```golang
{
  sub := r.PathPrefix("/chaff").Subrouter()
  sub.Handle("", track).Methods("GET")
}
```
