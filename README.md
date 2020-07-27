# Go Chaff Tracker / Generator

[![GoDoc](https://img.shields.io/badge/go-documentation-blue.svg?style=flat-square)](https://pkg.go.dev/github.com/mikehelmick/go-chaff@v0.2.1?tab=doc)
[![Go](https://github.com/mikehelmick/go-chaff/workflows/Go/badge.svg?event=push)](https://github.com/mikehelmick/go-chaff/actions?query=workflow%3AGo)

This package provides the necessary tools to allow for your server to handle
chaff (fake) requests from clients. This technique can be used when you want to
guard against the fact that clients are connecting to your server is meaningful.

The tracker automatically captures metadata like average request time and
response size, with the aim of making a chaff request indistinguishable from a
real request. This is useful in situations where someone (e.g. server operator,
network peer) should not be able to glean information about the system from
requests, their size, or their frequency.

Clients periodically send "chaff" requests. They denote the request is chaff via
a header or similar identifier. If one of your goals is to obfuscate server
logs, a dedicated URL is not recommended as this will be easily distinguisable
in logs.

There are two components:

- a middleware function that implements tracking
- an `http.Handler` that serves the chaff requests

## Usage

1.  Option 1 - use a single handler, detect chaff based on a request property
    like a header. This is most useful when you don't trust the server operator
    and can have the performance hit of the branching logic in a single handler:

    ```go
    mux := http.NewServeMux()
    mux.Handle("/", tracker.HandleTrack(chaff.HeaderDetector("X-Chaff"), myHandler))
    ```

    In this example, requests to `/` are served normally and the tracker
    generates heuristics automatically. When a request includes an `X-Chaff`
    header, the handler sends a chaff response.

1.  Option 2 - create the tracker on specific routes and provide a dedicated
    chaff endpoint. This is useful when you trust the server operator, but not
    the network observer:

    ```go
    r := mux.NewRouter()
    tracker := chaff.New()
    defer tracker.Close()

    mux := http.NewServeMux()
    mux.Handle("/", tracker.Track())
    mux.Handle("/chaff", tracker.HandleChaff())
    ```
