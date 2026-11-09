package tui

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/three-agent-problem/lagrange/client"

	"github.com/three-agent-problem/halo/internal/lg"
)

// Wait is a task waiting on you.
type Wait struct {
	TaskID   string
	Asker    string
	Question string
	Since    time.Time
}

// Backend is where the TUI gets its events and sends what you do.
type Backend interface {
	// Stream calls fn for each activity entry until ctx ends.
	Stream(ctx context.Context, fn func(lg.Entry), connected func(bool, error)) error
	// Send starts a run: a prompt to the lead. It returns the root task ID.
	Send(ctx context.Context, text string) (string, error)
	// Waiting lists the tasks waiting on you, oldest first.
	Waiting(ctx context.Context) ([]Wait, error)
	// Answer answers a waiting task; decision is "approve", "reject" or "".
	Answer(ctx context.Context, taskID, text, decision string) error
	// Cancel cancels a task.
	Cancel(ctx context.Context, taskID string) error
	// Online counts the roster's agents with a tunnel up.
	Online(ctx context.Context, handles []string) (int, error)
	// Commits lists the commits of run short since it started.
	Commits(ctx context.Context, short string, since time.Time) ([]Commit, error)
	// Diff returns a commit's git show output.
	Diff(ctx context.Context, sha string) (string, error)
}

// Live is the Backend of a Lagrange server.
type Live struct {
	Server, Key, Human, Lead string
	Repo                     string // the git repository the team works on
	Since                    int64  // where the stream starts: 0 replays everything
	c                        *client.Client
}

func (l *Live) client() *client.Client {
	if l.c == nil {
		l.c = client.New(l.Server, l.Key)
	}
	return l.c
}

// Stream implements Backend.
func (l *Live) Stream(ctx context.Context, fn func(lg.Entry), connected func(bool, error)) error {
	return lg.Stream(ctx, l.Server, l.Key, l.Since, fn, connected)
}

// Send implements Backend.
func (l *Live) Send(ctx context.Context, text string) (string, error) {
	res, err := l.client().Send(ctx, &client.Send{To: l.Lead, Text: text})
	if err != nil {
		return "", err
	}
	if res.Task != nil {
		return string(res.Task.ID), nil
	}
	return "", nil
}

// Waiting implements Backend.
func (l *Live) Waiting(ctx context.Context) ([]Wait, error) {
	ws, err := l.client().Waiting(ctx)
	if err != nil {
		return nil, err
	}
	var out []Wait
	for _, w := range ws {
		if w.Who != l.Human || w.Task == nil {
			continue
		}
		q := w.Task.Summary
		if w.Task.Task != nil && len(w.Task.Task.History) > 0 {
			q = lg.MessageText(w.Task.Task.History[0])
		}
		out = append(out, Wait{TaskID: w.Task.ID, Asker: w.Asker, Question: q, Since: w.Since})
	}
	// The API lists the longest wait first; keep that order.
	return out, nil
}

// Answer implements Backend.
func (l *Live) Answer(ctx context.Context, taskID, text, decision string) error {
	return l.client().Answer(ctx, taskID, &client.Answer{Text: text, Decision: decision})
}

// Cancel implements Backend.
func (l *Live) Cancel(ctx context.Context, taskID string) error {
	return l.client().Do(ctx, http.MethodPost, "/api/tasks/"+url.PathEscape(taskID)+"/cancel", nil, nil)
}

// Online implements Backend.
func (l *Live) Online(ctx context.Context, handles []string) (int, error) {
	addrs, err := l.client().Addresses(ctx)
	if err != nil {
		return 0, err
	}
	want := map[string]bool{}
	for _, h := range handles {
		want[h] = true
	}
	n := 0
	for _, a := range addrs {
		if want[a.Handle] && a.Online {
			n++
		}
	}
	return n, nil
}

// Commits implements Backend from the local git repository.
func (l *Live) Commits(ctx context.Context, short string, since time.Time) ([]Commit, error) {
	return GitCommits(ctx, l.Repo, short, since)
}

// Diff implements Backend.
func (l *Live) Diff(ctx context.Context, sha string) (string, error) {
	return GitShow(ctx, l.Repo, sha)
}
