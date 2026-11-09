package tui

import (
	"context"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Team is the roster this Halo hosts. It is nil when Halo only watches: another
// Halo hosts the team, or this directory isn't a git repository.
type Team interface {
	// Members is the roster, without you.
	Members() []Member
	// SetMembers saves the roster and restarts the agents with it.
	SetMembers(ctx context.Context, ms []Member) error
	// Land merges run short into the checked-out branch and removes its
	// branches and worktrees.
	Land(ctx context.Context, short string) (string, error)
}

type noteMsg string

// SetMembers replaces the roster, keeping what the TUI knows about agents that
// stay.
func (s *State) SetMembers(ms []Member) {
	keep := map[string]bool{s.Human: true}
	for _, m := range ms {
		keep[m.Handle] = true
		if a := s.Agents[m.Handle]; a != nil {
			a.Member = m
		} else {
			s.Agents[m.Handle] = &Agent{Member: m, State: "idle", active: map[string]bool{}, asking: map[string]bool{}}
		}
		switch m.Role {
		case "lead":
			s.Lead = m.Handle
		case "reviewer":
			s.Reviewer = m.Handle
		}
	}
	for h, a := range s.Agents {
		if !keep[h] && a.Role != "other" {
			delete(s.Agents, h)
		}
	}
	order := []string{s.Human, s.Lead}
	for _, m := range ms {
		if m.Handle != s.Lead {
			order = append(order, m.Handle)
		}
	}
	for _, h := range s.Order {
		if a := s.Agents[h]; a != nil && a.Role == "other" {
			order = append(order, h)
		}
	}
	s.Order = order
	if s.Agents[s.Focus] == nil {
		s.Focus = s.Lead
	}
}

var models = []string{"haiku", "sonnet", "opus"}

var agentName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// running reports whether a run is in flight.
func (m *model) running() bool { return m.st.Root != "" && !terminal(m.st.RootState) }

// editRoster checks that the roster can change now.
func (m *model) editRoster() bool {
	switch {
	case m.o.Team == nil:
		m.say("watch-only: " + nonEmpty(m.o.Watching, "this Halo doesn't host the team"))
		return false
	case m.running():
		m.say("the roster is locked while a run works")
		return false
	}
	return true
}

// members is the roster as the TUI shows it, without you.
func (m *model) members() []Member {
	var out []Member
	for _, h := range m.st.Order {
		if a := m.st.Agents[h]; a != nil && h != m.st.Human && a.Role != "other" {
			out = append(out, a.Member)
		}
	}
	return out
}

// applyRoster shows the new roster at once and restarts the team with it.
func (m *model) applyRoster(ms []Member, what string) tea.Cmd {
	m.st.SetMembers(ms)
	m.o.Handles = m.o.Handles[:0]
	for _, x := range ms {
		m.o.Handles = append(m.o.Handles, x.Handle)
	}
	team := m.o.Team
	m.say(what + " · restarting the team…")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, time.Minute)
		defer cancel()
		return doneMsg{what + " · team restarted", team.SetMembers(ctx, ms)}
	}
}

// rosterKey handles the roster panel's editing keys.
func (m *model) rosterKey(key string) (tea.Cmd, bool) {
	sel := m.ui.Cur[pRoster].sel
	var picked *Agent
	if sel < len(m.st.Order) {
		picked = m.st.Agents[m.st.Order[sel]]
	}
	switch key {
	case "n":
		if !m.editRoster() {
			return nil, true
		}
		m.mode = modeName
		return m.startInput("name the new agent (e.g. sonnet-3, opus-2), then enter"), true
	case "m":
		if picked == nil || picked.Role == "human" || picked.Role == "other" || !m.editRoster() {
			return nil, true
		}
		ms := m.members()
		for i := range ms {
			if ms[i].Handle == picked.Handle {
				for j, x := range models {
					if x == ms[i].Model {
						ms[i].Model = models[(j+1)%len(models)]
						break
					}
				}
				if !contains(models, ms[i].Model) {
					ms[i].Model = "sonnet"
				}
				return m.applyRoster(ms, m.st.Short(picked.Handle)+" → "+ms[i].Model), true
			}
		}
	case "d":
		if picked == nil || !m.editRoster() {
			return nil, true
		}
		if picked.Role != "worker" {
			m.say("only workers can be removed; press m to change the model of " + m.st.Short(picked.Handle))
			return nil, true
		}
		ms := m.members()
		workers := 0
		for _, x := range ms {
			if x.Role == "worker" {
				workers++
			}
		}
		if workers < 2 {
			m.say("the team needs at least one worker")
			return nil, true
		}
		if m.deleteArm != picked.Handle || time.Since(m.deleteAt) > 3*time.Second {
			m.deleteArm, m.deleteAt = picked.Handle, time.Now()
			m.say("press d again to remove " + m.st.Short(picked.Handle))
			return nil, true
		}
		m.deleteArm = ""
		var out []Member
		for _, x := range ms {
			if x.Handle != picked.Handle {
				out = append(out, x)
			}
		}
		return m.applyRoster(out, "removed "+m.st.Short(picked.Handle)), true
	case "R":
		if !m.editRoster() {
			return nil, true
		}
		return m.applyRoster(m.members(), "restart"), true
	}
	return nil, false
}

// addAgent adds a worker named name; its model is guessed from the name.
func (m *model) addAgent(name string) tea.Cmd {
	name = strings.ToLower(strings.TrimSpace(name))
	if !agentName.MatchString(name) {
		m.say("use lowercase letters, digits and dashes")
		return nil
	}
	ms := m.members()
	for _, x := range ms {
		if x.Name == name {
			m.say(name + " is already on the team")
			return nil
		}
	}
	model := "sonnet"
	for _, x := range models {
		if strings.Contains(name, x) {
			model = x
		}
	}
	ms = append(ms, Member{Handle: m.st.Prefix + "-" + name, Name: name, Model: model, Role: "worker"})
	return m.applyRoster(ms, "added @"+name+" ("+model+")")
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// SetOnline marks who of the roster has a tunnel up.
func (s *State) SetOnline(handles []string) {
	on := map[string]bool{}
	for _, h := range handles {
		on[h] = true
	}
	for h, a := range s.Agents {
		if a.Role != "human" && a.Role != "other" {
			a.Online = on[h]
		}
	}
}
