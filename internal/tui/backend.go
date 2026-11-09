package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/three-agent-problem/lagrange/client"

	"github.com/three-agent-problem/halo/internal/ask"
	"github.com/three-agent-problem/halo/internal/lg"
)

// Wait is a task waiting on you.
type Wait struct {
	TaskID    string
	Asker     string
	Question  string
	Since     time.Time
	Questions []ask.Question // a multiple-choice form, when the asker sent one
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
	// Online lists the roster's agents with a tunnel up.
	Online(ctx context.Context, handles []string) ([]string, error)
	// Commits lists the commits of run short since it started.
	Commits(ctx context.Context, short string, since time.Time) ([]Commit, error)
	// Diff returns a commit's git show output.
	Diff(ctx context.Context, sha string) (string, error)
	// Runs lists your runs with the lead, newest first.
	Runs(ctx context.Context) ([]RunInfo, error)
	// History returns the activity log so far, oldest first.
	History(ctx context.Context) ([]lg.Entry, error)
	// Continue makes later prompts continue run root's conversation, so the lead
	// remembers it; "" starts a fresh one.
	Continue(root string)
	// Message sends the person's message to an agent during a run: to is the lead's
	// or a worker's handle, subtask the subtask it is about ("" for the worker's
	// current one). direct sends it straight to the worker, in its session;
	// otherwise the lead relays it. It fails when there is no such run or subtask.
	Message(ctx context.Context, to, subtask, text string, direct bool) error
}

// RunInfo is a past run, for /resume.
type RunInfo struct {
	ID, Title, State string
	Tasks            int
	Started, Updated time.Time
}

// Live is the Backend of a Lagrange server.
type Live struct {
	Server, Key, Human, Lead string
	Repo                     string // the git repository the team works on
	Since                    int64  // where the stream starts: 0 replays everything, -1 starts now
	// Token returns an address token (the lead's) for Message: A2A calls with
	// Halo's metadata need one, since the API key only works on /api. It is a func
	// because the hosted team may provision its tokens after the TUI starts. nil or
	// "" uses Key.
	Token func() string
	c     *client.Client
	mu    sync.Mutex
	root  string // the run whose conversation prompts continue
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
	send := &client.Send{To: l.Lead, Text: text}
	l.mu.Lock()
	root := l.root
	l.mu.Unlock()
	if root != "" {
		if t, err := l.client().Task(ctx, root); err == nil {
			send.ContextID = t.ContextID
		}
	}
	res, err := l.client().Send(ctx, send)
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
		q, wt := w.Task.Summary, Wait{TaskID: w.Task.ID, Asker: w.Asker, Since: w.Since}
		if w.Task.Task != nil && len(w.Task.Task.History) > 0 {
			q = lg.MessageText(w.Task.Task.History[0])
			wt.Questions = ask.Parse(w.Task.Task.History[0].Metadata)
		}
		wt.Question = q
		out = append(out, wt)
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
func (l *Live) Online(ctx context.Context, handles []string) ([]string, error) {
	addrs, err := l.client().Addresses(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, h := range handles {
		want[h] = true
	}
	var on []string
	for _, a := range addrs {
		if want[a.Handle] && a.Online {
			on = append(on, a.Handle)
		}
	}
	return on, nil
}

// Commits implements Backend from the local git repository.
func (l *Live) Commits(ctx context.Context, short string, since time.Time) ([]Commit, error) {
	return GitCommits(ctx, l.Repo, short, since)
}

// Diff implements Backend.
func (l *Live) Diff(ctx context.Context, sha string) (string, error) {
	return GitShow(ctx, l.Repo, sha)
}

// Runs implements Backend.
func (l *Live) Runs(ctx context.Context) ([]RunInfo, error) {
	rs, err := l.client().Runs(ctx, client.RunFilter{Participant: l.Lead, Limit: 50})
	if err != nil {
		return nil, err
	}
	var out []RunInfo
	for _, r := range rs {
		if r.Starter != l.Human {
			continue
		}
		out = append(out, RunInfo{ID: r.ID, Title: r.Title, State: r.State, Tasks: r.Tasks, Started: r.StartedAt, Updated: r.UpdatedAt})
	}
	return out, nil
}

// History implements Backend: it pages through GET /api/activity.
func (l *Live) History(ctx context.Context) ([]lg.Entry, error) {
	var all []lg.Entry
	var since int64
	for {
		var page struct{ Entries []lg.Entry }
		if err := l.client().Do(ctx, http.MethodGet, fmt.Sprintf("/api/activity?since=%d&limit=5000", since), nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Entries...)
		if len(page.Entries) < 5000 {
			return all, nil
		}
		since = page.Entries[len(page.Entries)-1].Seq
	}
}

// Continue implements Backend.
func (l *Live) Continue(root string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.root = root
}

// Message implements Backend: it sends the lead a message with Halo's
// human_message metadata, which the hosting Halo routes without a Claude turn of
// the lead, and returns the lead's refusal as an error.
func (l *Live) Message(ctx context.Context, to, subtask, text string, direct bool) error {
	meta := map[string]any{"kind": "human_message", "from": l.Human}
	if to = strings.TrimPrefix(strings.TrimSpace(to), "@"); to != "" {
		meta["to"] = to
	}
	if subtask = strings.TrimSpace(subtask); subtask != "" {
		meta["subtask"] = subtask
	}
	if direct {
		meta["direct"] = true
	}
	tok := l.Key
	if l.Token != nil {
		tok = cmp.Or(l.Token(), l.Key)
	}
	res, err := lg.Caller{Server: l.Server, Token: tok}.Send(ctx, lg.Call{To: l.Lead, Text: text, Meta: map[string]any{"halo": meta}})
	if err != nil {
		return err
	}
	if t, ok := res.(*a2a.Task); ok && t.Status.State != a2a.TaskStateCompleted {
		why := lg.TaskText(t)
		if why == "" {
			why = string(t.Status.State)
		}
		return errors.New(why)
	}
	return nil
}
