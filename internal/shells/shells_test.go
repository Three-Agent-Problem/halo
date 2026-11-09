package shells

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestShellCommand(t *testing.T) {
	for in, want := range map[string]string{
		`/bin/zsh -c source /Users/k/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && setopt NO_EXTENDED_GLOB 2>/dev/null || true && eval 'npm run dev -- --port 5173' < /dev/null && pwd -P >| /tmp/claude-ae35-cwd`: "npm run dev -- --port 5173",
		`/bin/zsh -c source /s/shell-snapshots/x.sh && eval 'go test ./...\012echo '"'"'done'"'"'' < /dev/null && pwd -P >| /tmp/c`:                                                                                                   "go test ./...\necho 'done'",
		`bash -c make serve`:          "make serve",
		`/bin/sh -c 'sleep 3; true'`:  "sleep 3; true",
		`node /x/claude-agent-acp.js`: "",
	} {
		got, ok := shellCommand(in)
		if (want != "") != ok || got != want {
			t.Errorf("shellCommand(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestAgentOf(t *testing.T) {
	argv := Wrap("halo-sonnet-1", []string{"claude-agent-acp"})
	if got := agentOf(strings.Join(argv, " ")); got != "halo-sonnet-1" {
		t.Fatalf("agentOf = %q", got)
	}
	if got := agentOf("/bin/zsh -c echo halo-agent:x"); got != "" {
		t.Fatalf("agentOf = %q", got)
	}
}

func TestParseEtime(t *testing.T) {
	for in, want := range map[string]time.Duration{"00:07": 7 * time.Second, "01:02:03": time.Hour + 2*time.Minute + 3*time.Second, "2-00:00:01": 48*time.Hour + time.Second} {
		if got, err := ParseEtime(in); err != nil || got != want {
			t.Errorf("ParseEtime(%q) = %v, %v", in, got, err)
		}
	}
}

// TestScan starts a fake agent with a Claude-style shell and a shell that
// listens, and finds both under this process.
func TestScan(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("no lsof")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	out := filepath.Join(dir, "task.output")
	script := `/bin/sh -c "true /x/shell-snapshots/s.sh 2>/dev/null || true && eval 'sleep 30; echo done' < /dev/null && pwd -P >| /dev/null" > ` + out + ` &
/bin/sh -c "python3 -c 'import socket,time;s=socket.socket();s.bind((\"127.0.0.1\",0));s.listen();time.sleep(30)'; true" &
wait
`
	agent := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(agent, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	argv := Wrap("halo-test", []string{"/bin/sh", agent})
	root := exec.Command(argv[0], argv[1:]...)
	root.Dir = dir
	root.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := root.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-root.Process.Pid, syscall.SIGKILL)
		_ = root.Wait()
	})
	ctx := context.Background()
	var got []Shell
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		if got, err = Scan(ctx, os.Getpid()); err != nil {
			t.Fatal(err)
		}
		if len(got) == 2 && len(got[0].Ports)+len(got[1].Ports) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 shells, got %+v", got)
	}
	var sleeper, server *Shell
	for i := range got {
		if got[i].Command == "sleep 30; echo done" {
			sleeper = &got[i]
		} else {
			server = &got[i]
		}
	}
	if sleeper == nil || server == nil {
		t.Fatalf("shells: %+v", got)
	}
	if sleeper.Agent != "halo-test" || sleeper.Out != out || sleeper.Cwd != dir || sleeper.Procs < 2 {
		t.Errorf("sleeper: %+v", *sleeper)
	}
	if len(server.Ports) != 1 || server.Ports[0] == 0 {
		t.Errorf("server ports: %+v", *server)
	}
	if err := Stop(ctx, sleeper.PID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = Scan(ctx, os.Getpid())
		if len(got) == 1 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("Stop left %+v", got)
}

func TestTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := Tail(p, 8); got != "three\n" {
		t.Fatalf("Tail = %q", got)
	}
	if got, _ := Tail(p, 100); got != "one\ntwo\nthree\n" {
		t.Fatalf("Tail = %q", got)
	}
}
