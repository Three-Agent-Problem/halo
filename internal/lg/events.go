// Package lg holds what Halo needs from a Lagrange server beyond its client package:
// the activity stream and A2A calls made as an address.
package lg

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Entry is one activity log entry, as GET /api/events sends it.
type Entry struct {
	Seq          int64          `json:"seq"`
	At           time.Time      `json:"at"`
	Kind         string         `json:"kind"`
	TaskID       string         `json:"taskId,omitempty"`
	ParentTaskID string         `json:"parentTaskId,omitempty"`
	ContextID    string         `json:"contextId,omitempty"`
	Sender       string         `json:"sender,omitempty"`
	Recipient    string         `json:"recipient,omitempty"`
	State        string         `json:"state,omitempty"`
	Summary      string         `json:"summary,omitempty"`
	Data         map[string]any `json:"data,omitempty"`
}

// Str returns a string field of Data.
func (e Entry) Str(key string) string {
	s, _ := e.Data[key].(string)
	return s
}

// Bool returns a boolean field of Data.
func (e Entry) Bool(key string) bool {
	b, _ := e.Data[key].(bool)
	return b
}

// Num returns a numeric field of Data.
func (e Entry) Num(key string) float64 {
	n, _ := e.Data[key].(float64)
	return n
}

// Stream reads the workspace's activity from since (0: everything; -1: from now) and
// calls fn for each entry, reconnecting with backoff until ctx ends. connected, if not
// nil, is told when the stream comes up or drops.
func Stream(ctx context.Context, server, key string, since int64, fn func(Entry), connected func(bool, error)) error {
	backoff := time.Second
	for {
		last, err := streamOnce(ctx, server, key, since, fn, connected)
		if last > since {
			since, backoff = last, time.Second
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if connected != nil {
			connected(false, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 15*time.Second)
	}
}

func streamOnce(ctx context.Context, server, key string, since int64, fn func(Entry), connected func(bool, error)) (int64, error) {
	u := strings.TrimRight(server, "/") + "/api/events"
	if since >= 0 {
		u += "?since=" + strconv.FormatInt(since, 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return since, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return since, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body struct{ Error string }
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return since, fmt.Errorf("events: %s %s", resp.Status, body.Error)
	}
	if connected != nil {
		connected(true, nil)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e Entry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		since = e.Seq
		fn(e)
	}
	return since, sc.Err()
}
