package host

import (
	"context"
	"iter"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
	"github.com/three-agent-problem/lagrange/acp"
)

// talk answers the person's question about a run that is over (or one this Halo
// no longer holds, after a restart). The lead answers in a Claude turn of the
// run's own session, which remembers the run. No run starts and nothing changes.
// The answer completes the btw task, like an aside's answer.
func (l *lead) talk(ctx context.Context, ec *a2asrv.ExecutorContext, m meta) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
			return
		}
		fail := func(text string) {
			msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(text))
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateFailed, msg), nil)
		}
		runCtx := l.contextOf(ctx, m.Run)
		if runCtx == "" {
			fail("Halo doesn't have that run's conversation any more: send it as a prompt.")
			return
		}
		// The btw task has its own A2A context; its turn runs in the run's session.
		l.sess.alias(ec.ContextID, runCtx)
		defer l.sess.unalias(ec.ContextID)

		from := strings.TrimPrefix(nonEmpty(m.From, l.h.st.Human, "human"), "@")
		turn := continued(ec, talkPrompt(from, textOf(ec.Message)))
		var answer strings.Builder
		var end a2a.Event
		for ev, err := range l.exec.Execute(ctx, turn) {
			if err != nil {
				yield(nil, err)
				return
			}
			if a, ok := ev.(*a2a.TaskArtifactUpdateEvent); ok && a.Artifact != nil && a.Artifact.Name != acp.ThoughtsArtifact {
				for _, p := range a.Artifact.Parts {
					answer.WriteString(p.Text())
				}
			}
			if final(ev) {
				end = ev
				break
			}
			if !yield(ev, nil) {
				return
			}
		}
		if text, failed := failure(end); failed {
			fail(nonEmpty(text, "the lead couldn't answer"))
			return
		}
		if end == nil {
			return
		}
		text := strings.TrimSpace(answer.String())
		msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(nonEmpty(text, "(the lead had nothing to add)")))
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, msg), nil)
	}
}

func talkPrompt(from, text string) string {
	return "@" + from + " asks about this run, on the side (no new run starts): " + strings.TrimSpace(text) + "\n\n" +
		"Answer from what you know about the run; read files or git history to check if you need to. Keep it short and plain: " +
		"what happened, why, and what they can do next. Don't plan, delegate, call halo's run tools or change code. " +
		"If they want something changed, say they can send it as a new run (/new)."
}

// contextOf returns the A2A context of run id: the lead's, or Lagrange's record of
// the root task after a restart.
func (l *lead) contextOf(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	l.mu.Lock()
	cx := l.ctxOf[id]
	l.mu.Unlock()
	if cx != "" || l.h.api == nil {
		return cx
	}
	if t, err := l.h.api.Task(ctx, id); err == nil {
		return t.ContextID
	}
	return ""
}

// leadSessions is the lead's a2aacp.SessionStore: one Claude session per A2A
// context, saved on disk, where a talk's context borrows its run's session.
type leadSessions struct {
	f       *sessionFile
	mu      sync.Mutex
	aliases map[string]string // A2A context -> the context whose session it uses
}

func (s *leadSessions) key(contextID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.aliases[contextID]; a != "" {
		contextID = a
	}
	return "lead " + contextID
}

func (s *leadSessions) alias(contextID, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aliases == nil {
		s.aliases = map[string]string{}
	}
	s.aliases[contextID] = to
}

func (s *leadSessions) unalias(contextID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.aliases, contextID)
}

func (s *leadSessions) Get(_ context.Context, contextID string) (a2aacp.Session, bool, error) {
	return s.f.get(s.key(contextID))
}

func (s *leadSessions) Put(_ context.Context, contextID string, sess a2aacp.Session) error {
	return s.f.put(s.key(contextID), sess)
}
