package lg

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const wait = 5 * time.Second

type reqInfo struct {
	auth, accept, path string
	since              string
	hasSince           bool
}

func info(r *http.Request) reqInfo {
	return reqInfo{
		auth:     r.Header.Get("Authorization"),
		accept:   r.Header.Get("Accept"),
		path:     r.URL.Path,
		since:    r.URL.Query().Get("since"),
		hasSince: r.URL.Query().Has("since"),
	}
}

type conn struct {
	ok  bool
	err error
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wait):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func stop(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	if err := recv(t, done, "Stream to return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream returned %v, want context.Canceled", err)
	}
}

func TestStreamParsing(t *testing.T) {
	reqs := make(chan reqInfo, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs <- info(r)
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, l := range []string{
			`: comment`,
			`event: x`,
			`id: 3`,
			`data: {not json`,
			`data: {"seq":1,"kind":"a","sender":"alice","data":{"s":"v","b":true,"n":2.5}}`,
			`data: ["wrong","shape"]`,
			`data: {"seq":2,"kind":"b","sender":"bob"}`,
		} {
			fmt.Fprint(w, l+"\n\n")
			fl.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	for _, tc := range []struct {
		since    int64
		hasSince bool
		want     string
	}{{0, true, "0"}, {-1, false, ""}} {
		ctx, cancel := context.WithCancel(context.Background())
		entries := make(chan Entry, 8)
		done := make(chan error, 1)
		go func() {
			done <- Stream(ctx, srv.URL+"/", "k3y", tc.since, func(e Entry) { entries <- e }, nil)
		}()

		ri := recv(t, reqs, "request")
		if ri.auth != "Bearer k3y" || ri.accept != "text/event-stream" || ri.path != "/api/events" {
			t.Errorf("since=%d: bad request %+v", tc.since, ri)
		}
		if ri.hasSince != tc.hasSince || ri.since != tc.want {
			t.Errorf("since=%d: query since=%q present=%v, want %q present=%v", tc.since, ri.since, ri.hasSince, tc.want, tc.hasSince)
		}

		e1 := recv(t, entries, "entry 1")
		e2 := recv(t, entries, "entry 2")
		if e1.Seq != 1 || e1.Kind != "a" || e1.Sender != "alice" {
			t.Errorf("entry 1 = %+v", e1)
		}
		if e2.Seq != 2 || e2.Kind != "b" || e2.Sender != "bob" {
			t.Errorf("entry 2 = %+v", e2)
		}
		if e1.Str("s") != "v" || !e1.Bool("b") || e1.Num("n") != 2.5 {
			t.Errorf("accessors on %+v", e1.Data)
		}
		// Missing keys and wrong types give zero values.
		if e1.Str("missing") != "" || e1.Bool("missing") || e1.Num("missing") != 0 {
			t.Error("missing keys should be zero")
		}
		if e1.Str("b") != "" || e1.Bool("s") || e1.Num("s") != 0 {
			t.Error("wrong types should be zero")
		}
		if e2.Str("s") != "" || e2.Bool("b") || e2.Num("n") != 0 {
			t.Error("nil Data should give zero values")
		}
		select {
		case e := <-entries:
			t.Errorf("unexpected extra entry %+v", e)
		default:
		}
		stop(t, cancel, done)
	}
}

func TestStreamReconnectResumesFromSeq(t *testing.T) {
	reqs := make(chan reqInfo, 4)
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++ // handlers for this client run sequentially
		reqs <- info(r)
		fl := w.(http.Flusher)
		if n == 1 {
			fmt.Fprint(w, "data: {\"seq\":1,\"kind\":\"k\"}\n\ndata: {\"seq\":2,\"kind\":\"k\"}\n\n")
			fl.Flush()
			return
		}
		fmt.Fprint(w, "data: {\"seq\":3,\"kind\":\"k\"}\n\n")
		fl.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	entries := make(chan Entry, 8)
	conns := make(chan conn, 8)
	done := make(chan error, 1)
	go func() {
		done <- Stream(ctx, srv.URL, "k", 0, func(e Entry) { entries <- e }, func(ok bool, err error) { conns <- conn{ok, err} })
	}()

	if ri := recv(t, reqs, "first request"); ri.since != "0" {
		t.Errorf("first since = %q, want 0", ri.since)
	}
	for _, seq := range []int64{1, 2, 3} {
		if e := recv(t, entries, fmt.Sprintf("seq %d", seq)); e.Seq != seq {
			t.Errorf("got seq %d, want %d", e.Seq, seq)
		}
	}
	if ri := recv(t, reqs, "second request"); ri.since != "2" || !ri.hasSince {
		t.Errorf("second since = %q, want 2", ri.since)
	}

	for i, want := range []bool{true, false, true} {
		c := recv(t, conns, fmt.Sprintf("connected call %d", i))
		if c.ok != want {
			t.Errorf("connected call %d = %v, want %v", i, c.ok, want)
		}
		// A clean close by the server reports (false, nil): scanner EOF is not an error.
		if c.err != nil {
			t.Errorf("connected call %d err = %v, want nil", i, c.err)
		}
	}
	stop(t, cancel, done)
}

func TestStreamNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"bad key"}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	conns := make(chan conn, 8)
	done := make(chan error, 1)
	go func() {
		done <- Stream(ctx, srv.URL, "nope", 0, func(Entry) { t.Error("unexpected entry") }, func(ok bool, err error) { conns <- conn{ok, err} })
	}()

	c := recv(t, conns, "connected call")
	if c.ok || c.err == nil || !strings.Contains(c.err.Error(), "401") || !strings.Contains(c.err.Error(), "bad key") {
		t.Errorf("connected = %+v", c)
	}
	stop(t, cancel, done)
}
