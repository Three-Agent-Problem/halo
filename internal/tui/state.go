package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/three-agent-problem/halo/internal/lg"
	"github.com/three-agent-problem/halo/internal/plan"
)

// A2A task states as the activity log spells them.
const (
	stSubmitted = "TASK_STATE_SUBMITTED"
	stWorking   = "TASK_STATE_WORKING"
	stInput     = "TASK_STATE_INPUT_REQUIRED"
	stAuth      = "TASK_STATE_AUTH_REQUIRED"
	stCompleted = "TASK_STATE_COMPLETED"
	stFailed    = "TASK_STATE_FAILED"
	stRejected  = "TASK_STATE_REJECTED"
	stCanceled  = "TASK_STATE_CANCELED"
)

func terminal(s string) bool {
	return s == stCompleted || s == stFailed || s == stRejected || s == stCanceled
}

func short(state string) string {
	s := strings.TrimPrefix(state, "TASK_STATE_")
	if s == "INPUT_REQUIRED" {
		return "INPUT_REQ"
	}
	return s
}

// Member is a roster entry as the TUI sees it.
type Member struct {
	Handle, Name, Model, Role string
}

// Agent is what the TUI knows about one address.
type Agent struct {
	Member
	Online  bool
	State   string // idle, working, asking, waiting, failed, done
	Sub     string // the subtask it works on
	Log     []string
	Since   time.Time
	LastAct time.Time
	Usage   Usage             // tokens and cost, for agents this Halo hosts
	Feed    []*Block          // the live transcript, for agents this Halo hosts
	tools   map[string]*Block // Feed's tool calls by id
	active  map[string]bool   // tasks in progress
	asking  map[string]bool   // questions it sent that aren't answered
}

func (a *Agent) refresh() {
	switch {
	case len(a.asking) > 0:
		a.State = "asking"
	case len(a.active) > 0:
		if a.State != "working" && a.State != "waiting" {
			a.State = "working"
		}
	case a.State == "working" || a.State == "asking" || a.State == "waiting":
		a.State = "idle"
	}
}

func (a *Agent) log(line string) {
	a.Log = append(a.Log, line)
	if len(a.Log) > 500 {
		a.Log = a.Log[len(a.Log)-500:]
	}
}

// Task is a task of the current run.
type Task struct {
	ID, Parent, Sender, Recipient, State, Summary, Sub, Kind string
	Tools                                                    int
	Started, Ended                                           time.Time
	answer                                                   map[string]*strings.Builder
	answerOrder                                              []string
}

// Answer returns the task's streamed answer so far.
func (t *Task) Answer() string {
	var b strings.Builder
	for _, id := range t.answerOrder {
		b.WriteString(t.answer[id].String())
	}
	return strings.TrimSpace(b.String())
}

// Comm is one line of the agent-to-agent log.
type Comm struct {
	At             time.Time
	Kind           string // task, ask, reply, review, fail, hub
	From, To, Text string // Text is the whole message
	Sub            string // the subtask it is about
}

// Raw is one line of the raw event tail.
type Raw struct {
	Seq                    int64
	Kind, Who, State, Text string
	E                      lg.Entry
}

// Packet is a message travelling from one address to another through the hub.
type Packet struct {
	From, To, Kind, Label string
	Start                 time.Time
}

// Dot is a streamed chunk travelling from an agent to the hub.
type Dot struct {
	From  string
	Start time.Time
}

// PacketTime is how long a packet takes from sender to recipient.
const PacketTime = 1400 * time.Millisecond

// State folds the activity log into what the panes show.
type State struct {
	Human, Lead, Reviewer string
	Prefix                string
	Order                 []string
	Agents                map[string]*Agent

	Root      string
	RootAt    time.Time
	RootEnd   time.Time
	RootState string
	Prompt    string
	Tasks     map[string]*Task
	Plan      *plan.Plan
	planArt   string
	Follow    bool

	Comms   []Comm
	Commits []Commit
	Raw     []Raw
	Packets []Packet
	Dots    []Dot
	HubAt   time.Time

	Events    int
	evTimes   []time.Time
	Questions int
	Reviews   int
	Seq       int64

	// Pin, when set, is the only root task that starts a run (a resumed run).
	Pin string
	// Hint is shown while there is no run, e.g. how to resume the last one.
	Hint string
	// Shells are the processes the hosted agents run.
	Shells []Shell
	// Preview is the live preview of the run, when it is a web project.
	Preview Preview

	Focus       string
	focusAt     time.Time
	manualFocus time.Time
}

// NewState returns an empty state for a roster. members must include the lead,
// the reviewer and the human.
func NewState(prefix string, members []Member) *State {
	s := &State{Prefix: prefix, Agents: map[string]*Agent{}, Tasks: map[string]*Task{}, Follow: true}
	for _, m := range members {
		s.Agents[m.Handle] = &Agent{Member: m, State: "idle", active: map[string]bool{}, asking: map[string]bool{}}
		switch m.Role {
		case "human":
			s.Human = m.Handle
		case "lead":
			s.Lead = m.Handle
		case "reviewer":
			s.Reviewer = m.Handle
		}
	}
	// Orbit order: you on top, then the lead, then the rest as configured.
	s.Order = []string{s.Human, s.Lead}
	for _, m := range members {
		if m.Handle != s.Human && m.Handle != s.Lead {
			s.Order = append(s.Order, m.Handle)
		}
	}
	s.Focus = s.Lead
	return s
}

// Short is "@name" for a handle: the halo prefix dropped.
func (s *State) Short(handle string) string {
	if handle == "" {
		return "@?"
	}
	return "@" + strings.TrimPrefix(handle, s.Prefix+"-")
}

func (s *State) agent(handle string) *Agent {
	if a, ok := s.Agents[handle]; ok {
		return a
	}
	// Someone outside the roster joined the run: show them too.
	a := &Agent{Member: Member{Handle: handle, Name: handle, Model: "fg", Role: "other"}, State: "idle", active: map[string]bool{}, asking: map[string]bool{}}
	s.Agents[handle] = a
	s.Order = append(s.Order, handle)
	return a
}

func (s *State) model(handle string) string {
	if a, ok := s.Agents[handle]; ok {
		return a.Model
	}
	return "fg"
}

// Apply folds one activity entry in.
func (s *State) Apply(e lg.Entry, now time.Time) {
	if e.Seq > 0 && e.Seq <= s.Seq {
		return // seen already: a replay overlapping the live stream
	}
	if e.Seq > s.Seq {
		s.Seq = e.Seq
	}
	live := now.Sub(e.At) < 15*time.Second || e.At.IsZero()
	if !live {
		// History (a resume, a replay): keep the times it happened at.
		now = e.At
	}
	if e.Kind == "presence" {
		if a, ok := s.Agents[e.Recipient]; ok {
			a.Online = e.Num("sessions") > 0
		}
		return
	}
	if e.Kind == "task" && e.TaskID != "" && e.ParentTaskID == "" && e.Sender == s.Human && e.Recipient == s.Lead &&
		s.Tasks[e.TaskID] == nil && (s.Root == "" || s.Follow) && (s.Pin == "" || s.Pin == e.TaskID) {
		s.startRun(e, now)
	}
	if !s.inRun(e) {
		return
	}
	s.Events++
	s.evTimes = append(s.evTimes, now)
	s.raw(e)
	switch e.Kind {
	case "task":
		s.task(e, now, live)
	case "artifact":
		s.artifact(e, now, live)
	case "input":
		if t := s.Tasks[e.TaskID]; t != nil {
			from := e.Str("from")
			if from == "" {
				from = t.Sender
			}
			s.comm(now, "task", from, t.Recipient, nonEmpty(e.Str("content"), e.Summary), t.Sub)
			if live {
				s.packet(from, t.Recipient, "task", "↺ "+nonEmpty(t.Sub, "msg"), now)
			}
		}
	case "message":
		// A call that got a plain message back: the question and the answer at once.
		kind := "ask"
		s.comm(now, kind, e.Sender, e.Recipient, nonEmpty(e.Str("request"), e.Summary), plan.ParseTag(e.Summary))
		s.comm(now, "reply", e.Recipient, e.Sender, nonEmpty(e.Str("replyContent"), e.Str("reply")), plan.ParseTag(e.Summary))
		s.Questions++
		if live {
			s.packet(e.Sender, e.Recipient, kind, "?", now)
			s.packet(e.Recipient, e.Sender, "reply", "↳", now.Add(PacketTime))
		}
	}
}

func (s *State) startRun(e lg.Entry, now time.Time) {
	at := e.At
	if at.IsZero() || at.After(now) {
		at = now
	}
	s.Root, s.RootAt, s.RootEnd, s.RootState, s.Prompt = e.TaskID, at, time.Time{}, e.State, e.Summary
	s.Commits = nil
	s.Tasks = map[string]*Task{}
	s.Plan, s.planArt = nil, ""
	s.Packets, s.Dots = nil, nil
	s.Questions, s.Reviews = 0, 0
	for _, a := range s.Agents {
		a.State, a.Sub, a.Log = "idle", "", nil
		a.active, a.asking = map[string]bool{}, map[string]bool{}
	}
	if len(s.Comms) > 0 {
		s.Comms = append(s.Comms, Comm{At: now, Kind: "hub", Text: "──────────── new run ────────────"})
	}
}

func (s *State) inRun(e lg.Entry) bool {
	if s.Root == "" {
		return false
	}
	if e.TaskID != "" && (e.TaskID == s.Root || s.Tasks[e.TaskID] != nil) {
		return true
	}
	return e.ParentTaskID != "" && s.Tasks[e.ParentTaskID] != nil
}

func (s *State) raw(e lg.Entry) {
	who := s.Short(e.Recipient)
	if e.Sender != "" && (e.Kind == "message" || e.State == stSubmitted) {
		who = s.Short(e.Sender) + "→" + strings.TrimPrefix(s.Short(e.Recipient), "@")
	}
	text := e.Summary
	switch e.Kind {
	case "artifact":
		if e.Summary == plan.ArtifactName || e.Str("artifactId") == s.planArt {
			text = "halo.plan"
		} else {
			text = fmt.Sprintf("append +%dB", len(e.Str("content")))
		}
	case "message":
		text = nonEmpty(e.Str("reply"), e.Summary)
	}
	s.Raw = append(s.Raw, Raw{Seq: e.Seq, Kind: e.Kind, Who: who, State: short(e.State), Text: firstLine(text), E: e})
	if len(s.Raw) > 200 {
		s.Raw = s.Raw[len(s.Raw)-200:]
	}
}

// kindOf classifies a new task by who it goes to.
func (s *State) kindOf(from, to string) string {
	switch {
	case to == s.Human:
		return "ask"
	case to == s.Lead && from != s.Human:
		return "ask"
	case to == s.Reviewer:
		return "review"
	}
	return "task"
}

func (s *State) task(e lg.Entry, now time.Time, live bool) {
	t := s.Tasks[e.TaskID]
	first := t == nil
	if first {
		t = &Task{ID: e.TaskID, Parent: e.ParentTaskID, Sender: e.Sender, Recipient: e.Recipient, Summary: e.Summary,
			Sub: plan.ParseTag(e.Summary), Started: now, answer: map[string]*strings.Builder{}}
		t.Kind = s.kindOf(e.Sender, e.Recipient)
		if t.Sub == "" && t.Kind == "ask" && e.Recipient == s.Lead {
			if parent := s.Tasks[e.ParentTaskID]; parent != nil {
				t.Sub = parent.Sub
			}
		}
		s.Tasks[e.TaskID] = t
	}
	prev := t.State
	t.State = e.State
	from, to := t.Sender, t.Recipient
	if first || (prev == "" && e.State == stSubmitted) {
		label := nonEmpty(t.Sub, strings.ToUpper(t.Kind))
		switch t.Kind {
		case "ask":
			s.Questions++
			label = "?"
			if to == s.Human {
				label = "ASK"
			}
			s.agent(from).asking[t.ID] = true
			s.agent(from).refresh()
		case "review":
			s.Reviews++
			label = "REVIEW " + t.Sub
		}
		if t.ID == s.Root {
			label = "PROMPT"
		}
		s.comm(now, t.Kind, from, to, t.Summary, t.Sub)
		if live {
			s.packet(from, to, t.Kind, label, now)
		}
		a := s.agent(to)
		a.active[t.ID] = true
		if t.Sub != "" && t.Kind != "ask" {
			a.Sub = t.Sub
		}
		if a.State != "working" {
			a.Since = now
		}
		a.State = "working"
		a.LastAct = now
		a.refresh()
		if first && e.State == stSubmitted {
			return
		}
	}
	a := s.agent(to)
	switch {
	case e.State == stWorking:
		if tool, ok := strings.CutPrefix(e.Summary, "Finished "); ok {
			a.log("▸ " + tool)
			t.Tools++
			a.LastAct = now
			s.focusOn(to, now)
			if live {
				s.Dots = append(s.Dots, Dot{From: to, Start: now})
			}
		} else if e.Summary != "" && !strings.HasPrefix(e.Summary, "Waiting for") {
			a.log("“" + firstLine(e.Summary) + "”")
		}
		if a.State == "waiting" {
			a.State = "working"
		}
	case e.State == stInput || e.State == stAuth:
		if to == s.Human {
			a.State = "waiting"
		} else {
			// The agent asks its sender, e.g. a permission prompt.
			s.comm(now, "ask", to, from, nonEmpty(e.Summary, "needs input"), t.Sub)
			if live {
				s.packet(to, from, "ask", "?", now)
			}
			a.State = "asking"
		}
	case terminal(e.State):
		t.Ended = now
		delete(a.active, t.ID)
		if t.Kind == "ask" {
			delete(s.agent(from).asking, t.ID)
			s.agent(from).refresh()
		}
		ans := strings.TrimSpace(nonEmpty(t.Answer(), e.Summary))
		kind := "reply"
		switch {
		case e.State != stCompleted:
			kind = "fail"
			if ans == "" {
				ans = short(e.State)
			}
		case t.Kind == "review":
			kind = "review"
		}
		label := "DONE " + t.Sub
		switch {
		case kind == "fail":
			label = "✗ " + t.Sub
		case t.Kind == "review":
			label = "✓ " + t.Sub
			if strings.HasPrefix(strings.ToUpper(ans), "CHANGES") {
				kind, label = "fail", "✗ "+t.Sub
			}
		case t.Kind == "ask":
			label = "↳"
		case t.ID == s.Root:
			label = "✓ RUN"
		}
		s.comm(now, kind, to, from, nonEmpty(ans, "done"), t.Sub)
		if live {
			s.packet(to, from, kind, strings.TrimSpace(label), now)
		}
		if e.State == stFailed {
			a.State = "failed"
		}
		a.refresh()
		a.LastAct = now
		if t.ID == s.Root {
			s.RootState, s.RootEnd = e.State, now
		}
	}
	if t.ID == s.Root && !terminal(s.RootState) {
		s.RootState = e.State
	}
}

func (s *State) artifact(e lg.Entry, now time.Time, live bool) {
	t := s.Tasks[e.TaskID]
	if t == nil {
		return
	}
	id := e.Str("artifactId")
	if e.Summary == plan.ArtifactName || (id != "" && id == s.planArt) {
		s.planArt = id
		var p plan.Plan
		if json.Unmarshal([]byte(e.Str("content")), &p) == nil {
			s.setPlan(&p, now)
		}
		return
	}
	b := t.answer[id]
	if b == nil || !e.Bool("append") {
		if b == nil {
			t.answerOrder = append(t.answerOrder, id)
		}
		b = &strings.Builder{}
		t.answer[id] = b
	}
	b.WriteString(e.Str("content"))
	a := s.agent(t.Recipient)
	a.LastAct = now
	if live {
		s.Dots = append(s.Dots, Dot{From: t.Recipient, Start: now})
	}
}

// setPlan takes a new copy of the plan and logs what changed as hub notes.
func (s *State) setPlan(p *plan.Plan, now time.Time) {
	old := s.Plan
	s.Plan = p
	if old == nil {
		counts := map[string]int{}
		for _, st := range p.Subtasks {
			counts[st.Model]++
		}
		var mix []string
		for _, m := range plan.Ladder {
			if counts[m] > 0 {
				mix = append(mix, fmt.Sprintf("%d%s", counts[m], strings.ToUpper(m[:1])))
			}
		}
		s.comm(now, "hub", "", "", fmt.Sprintf("plan published · %d subtasks · %s", len(p.Subtasks), strings.Join(mix, " ")))
		return
	}
	if p.Approved && !old.Approved {
		s.comm(now, "hub", "", "", "plan approved · branch "+nonEmpty(p.Branch, "—"))
	}
	if p.MergedInto != "" && old.MergedInto == "" {
		s.comm(now, "hub", "", "", "✓ "+nonEmpty(p.Branch, "branch")+" merged into "+p.MergedInto)
	}
	for _, st := range p.Subtasks {
		was := old.Get(st.ID)
		if was == nil {
			s.comm(now, "hub", "", "", "plan + "+st.ID+" "+st.Title)
			continue
		}
		if was.Model != st.Model {
			s.comm(now, "hub", "", "", fmt.Sprintf("↑ escalate %s %s → %s", st.ID, was.Model, st.Model), st.ID)
		}
		if was.Status != st.Status {
			switch st.Status {
			case plan.Done:
				s.comm(now, "hub", "", "", fmt.Sprintf("✓ %s merged into %s", st.ID, nonEmpty(p.Branch, "the run branch")), st.ID)
			case plan.Conflict:
				s.comm(now, "hub", "", "", "✗ "+st.ID+" merge conflict", st.ID)
			}
		}
	}
}

func (s *State) comm(now time.Time, kind, from, to, text string, sub ...string) {
	text = strings.TrimSpace(text)
	if len(text) > 8000 {
		text = text[:8000] + "…"
	}
	c := Comm{At: now, Kind: kind, From: from, To: to, Text: text}
	if len(sub) > 0 {
		c.Sub = sub[0]
	}
	s.Comms = append(s.Comms, c)
	if len(s.Comms) > 300 {
		s.Comms = s.Comms[len(s.Comms)-300:]
	}
}

func (s *State) packet(from, to, kind, label string, at time.Time) {
	if from == "" || to == "" || from == to {
		return
	}
	s.agent(from).LastAct = at
	s.Packets = append(s.Packets, Packet{From: from, To: to, Kind: kind, Label: label, Start: at})
}

// focusOn moves the focus pane to an agent that is doing something, unless the
// user picked one recently or the current one is still busy.
func (s *State) focusOn(handle string, now time.Time) {
	if handle == s.Focus || now.Before(s.manualFocus) {
		return
	}
	cur := s.Agents[s.Focus]
	if cur == nil || now.Sub(s.focusAt) > 6*time.Second || (cur.State != "working" && cur.State != "asking") {
		s.Focus, s.focusAt = handle, now
	}
}

// CycleFocus moves the focus to the next agent and holds it there for a while.
func (s *State) CycleFocus(now time.Time, dir ...int) {
	step := 1
	if len(dir) > 0 && dir[0] < 0 {
		step = len(s.Order) - 1
	}
	for i, h := range s.Order {
		if h == s.Focus {
			for j := 1; j <= len(s.Order); j++ {
				n := s.Order[(i+j*step)%len(s.Order)]
				if n != s.Human {
					s.Focus = n
					break
				}
			}
			break
		}
	}
	s.manualFocus = now.Add(30 * time.Second)
}

// Prune drops packets and dots that arrived.
func (s *State) Prune(now time.Time) {
	keep := s.Packets[:0]
	for _, p := range s.Packets {
		if now.Sub(p.Start) < PacketTime+100*time.Millisecond {
			if now.Sub(p.Start) > PacketTime/2 && now.Sub(p.Start) < PacketTime/2+80*time.Millisecond {
				s.HubAt = now
			}
			keep = append(keep, p)
		}
	}
	s.Packets = keep
	dots := s.Dots[:0]
	for _, d := range s.Dots {
		if now.Sub(d.Start) < time.Second {
			dots = append(dots, d)
		}
	}
	s.Dots = dots
	cut := 0
	for cut < len(s.evTimes) && now.Sub(s.evTimes[cut]) > 24*time.Second {
		cut++
	}
	s.evTimes = s.evTimes[cut:]
}

// Rate returns events per second over the last 3 s, and per-second counts for
// the last n seconds.
func (s *State) Rate(now time.Time, n int) (float64, []int) {
	buckets := make([]int, n)
	recent := 0
	for _, t := range s.evTimes {
		d := now.Sub(t)
		if d < 3*time.Second {
			recent++
		}
		if i := n - 1 - int(d/time.Second); i >= 0 && i < n {
			buckets[i]++
		}
	}
	return float64(recent) / 3, buckets
}

// SubProgress estimates how far a running subtask is, from its worker's tool calls.
func (s *State) SubProgress(st *plan.Subtask, now time.Time) float64 {
	switch st.Status {
	case plan.Done:
		return 1
	case plan.Pending:
		return 0
	}
	n := 0
	if t := s.Tasks[st.Task]; t != nil {
		n = t.Tools
	}
	// Asymptotic: work never looks finished until the reviewer says so.
	return min(.92, 1-1/(1+float64(n)/6))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

func nonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
