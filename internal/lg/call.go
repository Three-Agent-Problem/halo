package lg

import (
	"context"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/three-agent-problem/lagrange/delegate"
)

// Caller calls other addresses through Lagrange as the address its token belongs to.
type Caller struct {
	Server string
	Token  string
}

// Call is one message to an address.
type Call struct {
	To        string         // handle
	Parent    string         // the platform task this call is part of
	Text      string         // the message
	Meta      map[string]any // message metadata, forwarded to the agent
	Immediate bool           // return as soon as the task exists
}

// Send sends the call and returns the task or message the address answered with.
func (c Caller) Send(ctx context.Context, call Call) (a2a.SendMessageResult, error) {
	cl, err := a2aclient.NewFromEndpoints(ctx,
		[]*a2a.AgentInterface{a2a.NewAgentInterface(strings.TrimRight(c.Server, "/")+"/a/"+call.To+"/rpc", a2a.TransportProtocolJSONRPC)},
		a2aclient.WithCallInterceptors(delegate.Interceptor(c.Token), parent(call.Parent)))
	if err != nil {
		return nil, err
	}
	defer cl.Destroy()
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(call.Text))
	msg.Metadata = call.Meta
	req := &a2a.SendMessageRequest{Message: msg}
	if call.Immediate {
		req.Config = &a2a.SendMessageConfig{ReturnImmediately: true}
	}
	res, err := cl.SendMessage(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("sending to @%s: %w", call.To, err)
	}
	return res, nil
}

// parent sets the Lagrange-Task header, so the call nests under the task it is part of.
type parent string

func (p parent) Before(ctx context.Context, req *a2aclient.Request) (context.Context, any, error) {
	if p != "" {
		req.ServiceParams[delegate.HeaderTask] = []string{string(p)}
	}
	return ctx, nil, nil
}

func (parent) After(context.Context, *a2aclient.Response) error { return nil }

// Text returns the readable answer of a send result.
func Text(res a2a.SendMessageResult) string {
	switch r := res.(type) {
	case *a2a.Message:
		return MessageText(r)
	case *a2a.Task:
		return TaskText(r)
	}
	return ""
}

// TaskText returns a task's answer: its artifacts' text, or its status message.
func TaskText(t *a2a.Task) string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	for _, art := range t.Artifacts {
		if strings.HasPrefix(art.Name, "halo.") {
			continue
		}
		for _, p := range art.Parts {
			b.WriteString(p.Text())
		}
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		return s
	}
	return MessageText(t.Status.Message)
}

// MessageText joins a message's text parts.
func MessageText(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range m.Parts {
		b.WriteString(p.Text())
	}
	return strings.TrimSpace(b.String())
}
