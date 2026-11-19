package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/plan"
)

// The plan studio is the plan panel's biggest size: the plan's subtasks, the
// visuals the lead showed (plan.Block, drawn in blocks.go) and your notes on
// them. Notes pile up as a draft and go to the lead in one batch (s), as the
// answer to its approval request, so a round of revisions costs one lead turn.

// studio is the studio's selection and your draft notes.
type studio struct {
	zone  int // 0: the subtask strip, 1: the items of the open block
	sub   int // the selected subtask
	block int // the open block; len(blocks) is the selected subtask's spec
	item  int // the selected item of the open block
	off   int // the open block's scroll
	notes []studioNote
}

// studioNote is one note for the lead. sub is the subtask it is about, if any;
// kind is "", "model" (text is the model) or "drop".
type studioNote struct {
	target, sub, kind, text string
}

// studioSubs is the plan the studio lists: the live plan (with statuses) when
// there is one, else the doc's.
func (s *State) studioSubs() []*plan.Subtask {
	if s.Plan != nil {
		return s.Plan.Subtasks
	}
	if s.Doc != nil && s.Doc.Plan != nil {
		return s.Doc.Plan.Subtasks
	}
	return nil
}

// fullSub is subtask id with its spec and acceptance, from the doc.
func (s *State) fullSub(id string) *plan.Subtask {
	var live, full *plan.Subtask
	if s.Plan != nil {
		live = s.Plan.Get(id)
	}
	if s.Doc != nil && s.Doc.Plan != nil {
		full = s.Doc.Plan.Get(id)
	}
	switch {
	case live == nil:
		return full
	case full == nil:
		return live
	}
	out := *live
	out.Spec, out.Acceptance = full.Spec, full.Acceptance
	return &out
}

func (s *State) blocks() []plan.Block {
	if s.Doc == nil {
		return nil
	}
	return s.Doc.Blocks
}

// clamp keeps the selection inside what there is.
func (st *studio) clamp(subs, blocks int, items func(int) int) {
	st.sub = max(0, min(st.sub, subs-1))
	st.block = max(0, min(st.block, blocks))
	if st.block < blocks {
		st.item = max(0, min(st.item, items(st.block)-1))
	} else {
		st.item = 0
	}
}

// studioPane draws the studio in w×h cells.
func (s *State) studioPane(w, h int, sc Screen) (string, string, []string) {
	st := sc.UI.Studio
	subs, bs := s.studioSubs(), s.blocks()
	st.clamp(len(subs), len(bs), func(i int) int { return blockItems(bs[i]) })
	approved := s.Plan != nil && s.Plan.Approved

	title := c("hi!", "PLAN STUDIO")
	var rbits []string
	if s.Doc != nil && s.Doc.Plan != nil && s.Doc.Plan.Version > 1 {
		rbits = append(rbits, fmt.Sprintf("v%d", s.Doc.Plan.Version))
		if d := marksText(s.DocMarks, s.DocRemoved); d != "" {
			rbits = append(rbits, d)
		}
	}
	switch {
	case approved:
		rbits = append(rbits, "approved")
	case sc.Approval:
		rbits = append(rbits, "waiting for you")
	default:
		rbits = append(rbits, "planning…")
	}
	right := c("dim", strings.Join(rbits, " · "))

	goal := ""
	if s.Plan != nil {
		goal = s.Plan.Goal
	} else if s.Doc != nil && s.Doc.Plan != nil {
		goal = s.Doc.Plan.Goal
	}
	var head []string
	for _, l := range wrapLines(goal, w-2, 2) {
		head = append(head, c("line2", "│ ")+c("mute", l))
	}
	head = append(head, "")
	head = append(head, s.strip(subs, st, w, sc)...)
	if len(s.DocRemoved) > 0 {
		head = append(head, c("fail", "  − dropped "+strings.Join(s.DocRemoved, ", ")))
	}
	head = append(head, "", s.studioTabs(bs, st, w), "")

	foot := s.studioFoot(st, w, approved, sc)
	areaH := max(3, h-len(head)-len(foot))
	var area drawn
	sid := ""
	if st.sub < len(subs) {
		sid = subs[st.sub].ID
	}
	if st.block < len(bs) {
		item := -1
		if st.zone == 1 {
			item = st.item
		}
		area = drawBlock(bs[st.block], w-2, sel{item: item, subtask: sid})
	} else {
		area = drawn{lines: s.specLines(sid, w-2, st)}
	}
	// Keep the selected item on screen.
	if st.zone == 1 && st.item < len(area.rows) {
		r := area.rows[st.item]
		if r < st.off {
			st.off = r
		}
		if r+3 > st.off+areaH {
			st.off = r + 3 - areaH
		}
	}
	st.off = max(0, min(st.off, len(area.lines)-areaH))
	body := area.lines[min(st.off, len(area.lines)):]
	if len(body) > areaH {
		body = body[:areaH]
	}
	lines := append([]string{}, head...)
	for _, l := range body {
		lines = append(lines, " "+l)
	}
	for len(lines) < h-len(foot) {
		lines = append(lines, "")
	}
	return title, right, append(lines, foot...)
}

func marksText(marks map[string]string, removed []string) string {
	n, ch := 0, 0
	for _, m := range marks {
		if m == "+" {
			n++
		} else {
			ch++
		}
	}
	var out []string
	if n > 0 {
		out = append(out, fmt.Sprintf("%d new", n))
	}
	if ch > 0 {
		out = append(out, fmt.Sprintf("%d changed", ch))
	}
	if len(removed) > 0 {
		out = append(out, fmt.Sprintf("%d dropped", len(removed)))
	}
	return strings.Join(out, " ")
}

// strip is the subtasks as chips, wrapped to w: the id in its model's color, the
// title, a revision mark and how many notes you wrote on it.
func (s *State) strip(subs []*plan.Subtask, st *studio, w int, sc Screen) []string {
	var lines []string
	cur, curW := "", 0
	for i, sub := range subs {
		id := " " + sub.ID + " "
		idS := badge(modelKey(sub.Model), id)
		if i == st.sub && st.zone == 0 {
			idS = c("cursor", "▸"+sub.ID+" ")
		}
		chip := idS + " " + c(pick(i == st.sub, "hi!", "fg"), fit(sub.Title, min(24, ansi.StringWidth(sub.Title)))) + c("dim", " "+sub.Model)
		if sub.Status != "" && sub.Status != plan.Pending {
			chip += " " + c("mute", statusIcon[sub.Status])
		}
		if m := s.DocMarks[sub.ID]; m != "" {
			chip += " " + c(pick(m == "+", "haiku!", "hub!"), m)
		}
		if n := countNotes(st.notes, sub.ID); n > 0 {
			chip += " " + c("ask!", fmt.Sprintf("✎%d", n))
		}
		cw := ansi.StringWidth(chip)
		if curW > 0 && curW+3+cw > w {
			lines = append(lines, cur)
			cur, curW = "", 0
		}
		if curW > 0 {
			cur += "   "
			curW += 3
		}
		cur += chip
		curW += cw
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		lines = []string{c("mute", "no subtasks yet · planning… "+string(spin[sc.Frame%len(spin)]))}
	}
	return lines
}

func countNotes(ns []studioNote, sub string) int {
	n := 0
	for _, x := range ns {
		if x.sub == sub {
			n++
		}
	}
	return n
}

// studioTabs names the blocks, then the spec of the selected subtask.
func (s *State) studioTabs(bs []plan.Block, st *studio, w int) string {
	var parts []string
	for i, b := range bs {
		label := fmt.Sprintf("%d %s", i+1, nonEmpty(b.Title, b.Kind))
		if i == st.block {
			parts = append(parts, c("hub!", "[ "+label+" ]"))
		} else {
			parts = append(parts, c("mute", label))
		}
	}
	spec := "spec"
	if st.block == len(bs) {
		parts = append(parts, c("hub!", "[ "+spec+" ]"))
	} else {
		parts = append(parts, c("mute", spec))
	}
	l := strings.Join(parts, c("line2", "  │  "))
	if len(bs) == 0 {
		l += c("dim", "   the lead showed no visuals")
	}
	return fit(l, w)
}

// specLines is the selected subtask in full: spec, acceptance, deps, and your
// notes on it.
func (s *State) specLines(id string, w int, st *studio) []string {
	sub := s.fullSub(id)
	if sub == nil {
		return []string{c("dim", "no subtask selected")}
	}
	head := badge(modelKey(sub.Model), " "+sub.ID+" ") + " " + c("hi!", sub.Title) + c("dim", " · "+sub.Model)
	if len(sub.Deps) > 0 {
		head += c("dim", " · after "+strings.Join(sub.Deps, ", "))
	}
	lines := []string{head, ""}
	section := func(name, text string) {
		lines = append(lines, c("fg!", name))
		if strings.TrimSpace(text) == "" {
			lines = append(lines, c("dim", "  (not in the doc yet)"))
		}
		for _, l := range wrapLines(text, w-2, 0) {
			lines = append(lines, "  "+c("fg", l))
		}
		lines = append(lines, "")
	}
	section("spec", sub.Spec)
	section("acceptance", sub.Acceptance)
	if sub.Note != "" {
		lines = append(lines, c("fg!", "latest"), "  "+c("mute", sub.Note), "")
	}
	for _, n := range st.notes {
		if n.sub == sub.ID {
			lines = append(lines, c("ask", "✎ ")+c("fg", n.say()))
		}
	}
	return lines
}

// say is the note as the lead reads it.
func (n studioNote) say() string {
	switch n.kind {
	case "model":
		return "use " + n.text
	case "drop":
		return "drop it"
	}
	return n.text
}

func (s *State) studioFoot(st *studio, w int, approved bool, sc Screen) []string {
	var out []string
	if n := len(st.notes); n > 0 {
		out = append(out, c("line2", strings.Repeat("╌", w)))
		shown := st.notes
		if len(shown) > 3 {
			shown = shown[len(shown)-3:]
		}
		for _, x := range shown {
			out = append(out, fit(c("ask", "✎ ")+c("mute", x.target+": ")+c("fg", x.say()), w))
		}
		if n > 3 {
			out = append(out, c("dim", fmt.Sprintf("  … and %d more", n-3)))
		}
		send := "s sends them to the lead"
		if sc.Approval {
			send = "s sends them to the lead, who revises the plan · y approves as it is"
		}
		out = append(out, c("ask!", fmt.Sprintf("%d note%s pending", n, pick(n == 1, "", "s")))+c("dim", " · "+send+" · u undo"))
	} else if sc.Approval && !approved {
		out = append(out, "", c("ask", "waiting for your approval · y approve · or write notes with c, then s"))
	}
	return out
}

// ── keys ─────────────────────────────────────────────────────────────────────

// studioKey handles a key in the studio; ok is false for keys it leaves to the
// plan panel.
func (m *model) studioKey(key string) (tea.Cmd, bool) {
	st, s := m.ui.Studio, m.st
	subs, bs := s.studioSubs(), s.blocks()
	st.clamp(len(subs), len(bs), func(i int) int { return blockItems(bs[i]) })
	approved := s.Plan != nil && s.Plan.Approved
	switch key {
	case "h", "left":
		st.zone, st.sub = 0, max(0, st.sub-1)
	case "l", "right":
		st.zone, st.sub = 0, min(len(subs)-1, st.sub+1)
	case "j", "down", "k", "up":
		d := 1
		if key == "k" || key == "up" {
			d = -1
		}
		if st.block == len(bs) { // the spec scrolls
			st.off = max(0, st.off+d)
			return nil, true
		}
		if st.zone == 0 {
			st.zone = 1
			return nil, true
		}
		st.item = max(0, min(blockItems(bs[st.block])-1, st.item+d))
		// A node of a subtask selects the subtask too.
		if _, sub := itemLabel(bs[st.block], st.item); sub != "" {
			if i := slices.IndexFunc(subs, func(x *plan.Subtask) bool { return x.ID == sub }); i >= 0 {
				st.sub = i
			}
		}
	case "[", "]", "tab", "shift+tab":
		d := 1
		if key == "[" || key == "shift+tab" {
			d = -1
		}
		st.block = (st.block + d + len(bs) + 1) % (len(bs) + 1)
		st.item, st.off = 0, 0
	case "enter":
		st.block, st.off = len(bs), 0
	case "c":
		target, sub := m.studioTarget()
		if target == "" {
			m.say("nothing selected to write a note on")
			return nil, true
		}
		m.noteFor = studioNote{target: target, sub: sub}
		m.mode = modeStudioNote
		return m.startInput("your note on " + target + ", then enter"), true
	case "m":
		if approved || st.sub >= len(subs) {
			return nil, false
		}
		sub := subs[st.sub]
		now := sub.Model
		for _, n := range st.notes {
			if n.sub == sub.ID && n.kind == "model" {
				now = n.text
			}
		}
		next := plan.Ladder[0]
		if i := slices.Index(plan.Ladder, now); i >= 0 {
			next = plan.Ladder[(i+1)%len(plan.Ladder)]
		}
		st.notes = slices.DeleteFunc(st.notes, func(n studioNote) bool { return n.sub == sub.ID && n.kind == "model" })
		if next != sub.Model {
			st.notes = append(st.notes, studioNote{target: sub.ID + " " + sub.Title, sub: sub.ID, kind: "model", text: next})
		}
	case "x":
		if approved || st.sub >= len(subs) {
			return nil, false
		}
		sub := subs[st.sub]
		had := len(st.notes)
		st.notes = slices.DeleteFunc(st.notes, func(n studioNote) bool { return n.sub == sub.ID && n.kind == "drop" })
		if len(st.notes) == had {
			st.notes = append(st.notes, studioNote{target: sub.ID + " " + sub.Title, sub: sub.ID, kind: "drop"})
		}
	case "u":
		if len(st.notes) == 0 {
			m.say("no notes to undo")
			return nil, true
		}
		st.notes = st.notes[:len(st.notes)-1]
	case "s":
		return m.sendNotes(), true
	case "y":
		w := m.approvalWait()
		if w == nil {
			return nil, false
		}
		if n := len(st.notes); n > 0 {
			m.confirm = &confirm{title: "APPROVE", q: fmt.Sprintf("Approve and drop your %d note%s?", n, pick(n == 1, "", "s")),
				what: "n goes back; s sends the notes instead", yes: func() tea.Cmd { st.notes = nil; return m.approvePlan(w) }}
			return nil, true
		}
		return m.approvePlan(w), true
	default:
		return nil, false
	}
	return nil, true
}

// studioTarget is what a note written now is about.
func (m *model) studioTarget() (string, string) {
	st, s := m.ui.Studio, m.st
	subs, bs := s.studioSubs(), s.blocks()
	if st.zone == 1 && st.block < len(bs) {
		return itemLabel(bs[st.block], st.item)
	}
	if st.sub < len(subs) {
		return subs[st.sub].ID + " " + subs[st.sub].Title, subs[st.sub].ID
	}
	return "", ""
}

// approvalWait is the lead's plan approval waiting on you, or nil.
func (m *model) approvalWait() *Wait {
	for i := range m.waits {
		if m.waits[i].Approval {
			return &m.waits[i]
		}
	}
	return nil
}

func (m *model) approvePlan(w *Wait) tea.Cmd {
	id := w.TaskID
	m.dropWait(id)
	ctx, b := m.ctx, m.o.Backend
	return func() tea.Msg { return doneMsg{"plan approved", b.Answer(ctx, id, "", "approve")} }
}

// notesText is the notes as one message to the lead.
func notesText(ns []studioNote, version int) string {
	var b strings.Builder
	if version > 0 {
		fmt.Fprintf(&b, "My notes on the plan (version %d):\n", version)
	} else {
		b.WriteString("My notes on the plan:\n")
	}
	for _, n := range ns {
		fmt.Fprintf(&b, "- %s: %s\n", n.target, n.say())
	}
	return strings.TrimSpace(b.String())
}

// sendNotes sends the draft: as the answer to the plan approval (a rejection the
// lead revises from), or, with nothing waiting, as a message to the lead.
func (m *model) sendNotes() tea.Cmd {
	st := m.ui.Studio
	if len(st.notes) == 0 {
		m.say("no notes yet · c writes one on what is selected")
		return nil
	}
	version := 0
	if m.st.Doc != nil && m.st.Doc.Plan != nil {
		version = m.st.Doc.Plan.Version
	} else if m.st.Plan != nil {
		version = m.st.Plan.Version
	}
	text := notesText(st.notes, version)
	n := len(st.notes)
	st.notes = nil
	ctx, b := m.ctx, m.o.Backend
	what := fmt.Sprintf("%d note%s sent to the lead", n, pick(n == 1, "", "s"))
	if w := m.approvalWait(); w != nil {
		id := w.TaskID
		m.dropWait(id)
		return func() tea.Msg { return doneMsg{what + " · it revises the plan", b.Answer(ctx, id, text, "reject")} }
	}
	lead := m.st.Lead
	return func() tea.Msg { return doneMsg{what, b.Message(ctx, lead, "", text, false)} }
}

// noteKey handles the note being typed.
func (m *model) noteKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeNormal
		m.clearInput()
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		m.clearInput()
		m.mode = modeNormal
		if text == "" {
			return m, nil
		}
		n := m.noteFor
		n.text, _ = m.expandImages(ExpandMentions(text, m.shortMap()))
		m.ui.Studio.notes = append(m.ui.Studio.notes, n)
		return m, nil
	}
	return m, m.typed(k)
}

// fetchDoc loads the plan doc when the log's copy was cut.
func (m *model) fetchDoc() tea.Cmd {
	if !m.st.DocWant || m.st.Root == "" || m.docLoading {
		return nil
	}
	m.docLoading, m.st.DocWant = true, false
	ctx, b, root := m.ctx, m.o.Backend, m.st.Root
	return func() tea.Msg {
		d, err := b.PlanDoc(ctx, root)
		return docMsg{root: root, doc: d, err: err}
	}
}

type docMsg struct {
	root string
	doc  *plan.Doc
	err  error
}

// openStudio opens the studio for a plan approval that just arrived, once per
// approval, unless you are in the middle of something.
func (m *model) openStudio(ws []Wait) {
	for _, w := range ws {
		if !w.Approval || m.studioFor[w.TaskID] {
			continue
		}
		if m.studioFor == nil {
			m.studioFor = map[string]bool{}
		}
		m.studioFor[w.TaskID] = true
		if m.mode != modeNormal || m.summary || m.reader != nil || m.conn != nil || m.picker != nil || m.ui.Help || m.confirm != nil {
			continue
		}
		m.ui.SetFocus(pPlan)
		m.ui.Size[pPlan] = sizeStudio
	}
}
