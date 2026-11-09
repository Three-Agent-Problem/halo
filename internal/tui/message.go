package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// msgTarget is who a message goes to: the agent's handle, the subtask it is
// about ("" for the worker's current one) and the route.
type msgTarget struct {
	to, sub string
	direct  bool
}

// msgHint is the input's placeholder for the target and route.
func (m *model) msgHint() string {
	route := "via lead"
	if m.msg.direct {
		route = "direct"
	}
	tab := "tab: direct/lead, "
	if m.msg.to == m.st.Lead {
		tab = ""
	}
	return fmt.Sprintf("message %s (%s) — %senter send, esc cancel", m.st.Short(m.msg.to), route, tab)
}

// msgTargetOf resolves what m messages: the selected subtask and its worker in
// the plan pane, else the agent shown in the orbit or agent pane and the
// subtask it works on.
func (m *model) msgTargetOf() (msgTarget, bool) {
	if m.ui.Focus == pPlan {
		sel := m.ui.Cur[pPlan].sel
		if m.st.Plan == nil || sel < 0 || sel >= len(m.st.Plan.Subtasks) {
			return msgTarget{}, false
		}
		st := m.st.Plan.Subtasks[sel]
		if st.Worker == "" {
			return msgTarget{to: m.st.Lead, sub: st.ID}, m.st.Lead != ""
		}
		return msgTarget{to: st.Worker, sub: st.ID}, true
	}
	h := m.st.Focus
	a := m.st.Agents[h]
	if a == nil || h == m.st.Human || h == "" {
		return msgTarget{}, false
	}
	return msgTarget{to: h, sub: a.Sub}, true
}

// startMessage opens the input for a message, or says why it can't.
func (m *model) startMessage() tea.Cmd {
	t, ok := m.msgTargetOf()
	if !ok {
		m.say("no agent selected to message")
		return nil
	}
	m.msg, m.mode = t, modeMessage
	return m.startInput(m.msgHint())
}
