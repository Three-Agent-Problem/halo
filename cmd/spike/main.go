// Command spike checks the risky assumptions of the Halo design against a Lagrange
// server: a Claude Code worker with a chosen model and a per-session cwd, an MCP tool
// that turns into an A2A task to another address, and what /api/events shows of it.
//
//	LAGRANGE_SERVER=http://localhost:8190 LAGRANGE_KEY=lgk_... go run ./cmd/spike
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/three-agent-problem/lagrange/client"
	"github.com/three-agent-problem/lagrange/delegate"
	"github.com/three-agent-problem/lagrange/tunnel"
)

var (
	server = env("LAGRANGE_SERVER", "http://localhost:8190")
	key    = os.Getenv("LAGRANGE_KEY")
	suffix = time.Now().Format("150405")
	lead   = "spike-lead-" + suffix
	worker = "spike-haiku-" + suffix
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "spike:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	c := client.New(server, key)

	leadTok, err := c.CreateAgent(ctx, lead)
	if err != nil {
		return fmt.Errorf("create %s: %w", lead, err)
	}
	workerTok, err := c.CreateAgent(ctx, worker)
	if err != nil {
		return fmt.Errorf("create %s: %w", worker, err)
	}

	// Record the event stream.
	evf, _ := os.Create("spike-events.jsonl")
	defer evf.Close()
	go streamEvents(ctx, evf)

	// The lead: a stub that takes 20s to answer, to see a blocking MCP tool in action.
	leadExec := a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			q := textOf(ec.Message)
			fmt.Printf("[lead] got question (platform task %s): %q\n", delegate.Task(ctx), q)
			time.Sleep(20 * time.Second)
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("Lead says: yes, name the file greeting.txt instead.")), nil)
		}
	})
	go tunnel.Run(ctx, tunnel.ClientConfig{Server: server, Token: leadTok, Capacity: 4, Logger: log,
		Handler: serve(card("Spike lead", "Answers worker questions."), delegate.Executor(leadExec))})

	// The MCP server the worker's Claude session gets: ask_lead sends an A2A task to the
	// lead through Lagrange, as the worker, inside the worker's platform task.
	var current sync.Map // worker's local context ID -> platform task ID
	mcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mcpURL := "http://" + mcpLn.Addr().String() + "/mcp"
	go http.Serve(mcpLn, mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		ctxID := r.Header.Get("Halo-Context")
		s := mcp.NewServer(&mcp.Implementation{Name: "halo", Version: "spike"}, nil)
		type askIn struct {
			Question string `json:"question" jsonschema:"the question for the lead"`
		}
		type askOut struct {
			Answer string `json:"answer"`
		}
		mcp.AddTool(s, &mcp.Tool{Name: "ask_lead", Description: "Ask the lead engineer a question and wait for the answer."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in askIn) (*mcp.CallToolResult, askOut, error) {
				parent, _ := current.Load(ctxID)
				p, _ := parent.(string)
				fmt.Printf("[mcp] ask_lead from context %s (platform task %s): %q\n", ctxID, p, in.Question)
				ans, err := sendAs(ctx, workerTok, p, lead, in.Question)
				return nil, askOut{Answer: ans}, err
			})
		return s
	}, nil))

	// The worker: Claude Code over ACP, model haiku, its own directory per context.
	base, _ := os.MkdirTemp("", "halo-spike-")
	acpExec, err := a2aacp.NewExecutor(ctx, a2aacp.Process{Argv: []string{"claude-agent-acp"}, Dir: base, Stderr: os.Stderr}, &a2aacp.Config{
		AutoAnswer: a2aacp.ToolRules{Allow: []string{"*"}},
		Settings: func(context.Context, *a2asrv.ExecutorContext) (map[string]any, error) {
			return map[string]any{"model": env("SPIKE_MODEL", "haiku")}, nil
		},
		Session: func(ctx context.Context, ec *a2asrv.ExecutorContext) (a2aacp.SessionParams, error) {
			dir := filepath.Join(base, "ctx-"+ec.ContextID[:8])
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return a2aacp.SessionParams{}, err
			}
			srv, _ := json.Marshal(map[string]any{"type": "http", "name": "halo", "url": mcpURL,
				"headers": []map[string]string{{"name": "Halo-Context", "value": ec.ContextID}}})
			fmt.Printf("[worker] new session cwd=%s\n", dir)
			return a2aacp.SessionParams{Cwd: dir, MCPServers: []json.RawMessage{srv}}, nil
		},
	})
	if err != nil {
		return fmt.Errorf("acp: %w", err)
	}
	defer acpExec.Close()
	track := a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		current.Store(ec.ContextID, delegate.Task(ctx))
		return acpExec.Execute(ctx, ec)
	})
	go tunnel.Run(ctx, tunnel.ClientConfig{Server: server, Token: workerTok, Capacity: 4, Logger: log,
		Handler: serve(card("Spike haiku", "Claude Code on haiku."), delegate.Executor(cancelable{track, acpExec}))})

	time.Sleep(3 * time.Second) // tunnels up, cards fetched

	prompt := "Before doing anything else, call the ask_lead tool with the question 'Should the file be hello.txt?'. " +
		"Then create the file the lead tells you to, containing the word hi, in the current directory. " +
		"Finally run `pwd` and reply with one line: the file name, the pwd, and which model you are."
	res, err := c.Send(ctx, &client.Send{To: worker, Text: prompt})
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	id := string(res.Task.ID)
	fmt.Println("[me] task", id)
	for {
		t, err := c.Task(ctx, id)
		if err != nil {
			return err
		}
		if a2a.TaskState(t.State).Terminal() || t.State == string(a2a.TaskStateInputRequired) {
			fmt.Printf("[me] %s: %s\n", t.State, t.Summary)
			if t.Task != nil {
				for _, art := range t.Task.Artifacts {
					for _, p := range art.Parts {
						if p.Text() != "" {
							fmt.Println("[answer]", p.Text())
						}
					}
				}
			}
			break
		}
		time.Sleep(2 * time.Second)
	}
	entries, _ := filepath.Glob(filepath.Join(base, "ctx-*", "*"))
	fmt.Println("[fs]", entries)
	time.Sleep(2 * time.Second)
	return nil
}

type cancelable struct {
	a2asrv.AgentExecutorFunc
	exec *a2aacp.Executor
}

func (c cancelable) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return c.exec.Cancel(ctx, ec)
}

// sendAs sends text to an address as the token's owner, inside platform task parent, and
// returns the answer.
func sendAs(ctx context.Context, token, parent, to, text string) (string, error) {
	cl, err := a2aclient.NewFromEndpoints(ctx,
		[]*a2a.AgentInterface{a2a.NewAgentInterface(server+"/a/"+to+"/rpc", a2a.TransportProtocolJSONRPC)},
		a2aclient.WithCallInterceptors(delegate.Interceptor(token), parentHeader(parent)))
	if err != nil {
		return "", err
	}
	defer cl.Destroy()
	res, err := cl.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))})
	if err != nil {
		return "", err
	}
	switch r := res.(type) {
	case *a2a.Message:
		return textOf(r), nil
	case *a2a.Task:
		var b strings.Builder
		for _, art := range r.Artifacts {
			for _, p := range art.Parts {
				b.WriteString(p.Text())
			}
		}
		if b.Len() == 0 && r.Status.Message != nil {
			return textOf(r.Status.Message), nil
		}
		return b.String(), nil
	}
	return "", fmt.Errorf("unexpected result %T", res)
}

type parentHeader string

func (p parentHeader) Before(ctx context.Context, req *a2aclient.Request) (context.Context, any, error) {
	if p != "" {
		req.ServiceParams[delegate.HeaderTask] = []string{string(p)}
	}
	return ctx, nil, nil
}

func (parentHeader) After(context.Context, *a2aclient.Response) error { return nil }

func textOf(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range m.Parts {
		b.WriteString(p.Text())
	}
	return b.String()
}

func card(name, desc string) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name: name, Description: desc, Version: "0.0.1",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://agent/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities:        a2a.AgentCapabilities{Streaming: true},
		DefaultInputModes:   []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{{ID: "code", Name: "Code", Description: desc, Tags: []string{"coding"}}},
	}
}

func serve(c *a2a.AgentCard, exec a2asrv.AgentExecutor) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(c))
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(exec)))
	return mux
}

func streamEvents(ctx context.Context, w *os.File) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("[events]", err)
		return
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			fmt.Fprintln(w, line)
		}
	}
}
