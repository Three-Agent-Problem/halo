// Package shells finds the processes the hosted agents started: the shells
// Claude Code runs commands in, and the servers they leave listening. Each agent
// runs under a small sh whose command line names it (see [Wrap]), and the process
// tree under it tells which agent a shell belongs to.
package shells

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const marker = "halo-agent:"

// Wrap returns argv run under a sh that stays in the process tree with the
// agent's handle on its command line, so [Scan] can tell whose shells are whose.
// Stdio and signals pass through; the sh exits with the agent.
func Wrap(handle string, argv []string) []string {
	return append([]string{"/bin/sh", "-c", `"$@"; exit $?`, marker + handle}, argv...)
}

func agentOf(cmd string) string {
	i := strings.Index(cmd, " "+marker)
	if i < 0 || !strings.HasPrefix(cmd, "/bin/sh -c ") {
		return ""
	}
	a, _, _ := strings.Cut(cmd[i+1+len(marker):], " ")
	return a
}

// Shell is a command an agent runs.
type Shell struct {
	PID     int
	Agent   string // the agent's handle
	Command string // what the agent asked to run
	Kind    string // "shell", or "server" for a process listening on a port
	Cwd     string
	Started time.Time
	Ports   []int  // TCP ports the shell or its children listen on
	Out     string // the file its output goes to, if it is a file
	Procs   int    // processes in its tree, itself included
}

type proc struct {
	pid, ppid int
	started   time.Time
	cmd       string
}

// Scan lists the shells of the agents that are children of root (see [Wrap]).
func Scan(ctx context.Context, root int) ([]Shell, error) {
	ps, err := list(ctx)
	if err != nil {
		return nil, err
	}
	kids := map[int][]*proc{}
	for _, p := range ps {
		kids[p.ppid] = append(kids[p.ppid], p)
	}
	var out []Shell
	owner := map[int]int{}       // every pid in a shell's tree → the shell's index
	loose := map[int]looseProc{} // processes outside shells, servers if they listen
	for _, a := range kids[root] {
		agent := agentOf(a.cmd)
		if agent == "" {
			continue
		}
		// Walk the agent's tree; the topmost shell of a branch is one Shell.
		var walk func(p *proc, shell int)
		walk = func(p *proc, shell int) {
			if shell < 0 && p != a {
				if cmd, ok := shellCommand(p.cmd); ok {
					out = append(out, Shell{PID: p.pid, Agent: agent, Command: cmd, Kind: "shell", Started: p.started})
					shell = len(out) - 1
				}
			}
			switch {
			case shell >= 0:
				owner[p.pid] = shell
				out[shell].Procs++
			case p != a:
				loose[p.pid] = looseProc{p, agent}
			}
			for _, k := range kids[p.pid] {
				walk(k, shell)
			}
		}
		walk(a, -1)
	}
	if len(owner) == 0 && len(loose) == 0 {
		return nil, nil
	}
	for pid, ps := range listening(ctx) {
		i, ok := owner[pid]
		if !ok {
			lp, ok := loose[pid]
			if !ok {
				continue
			}
			// A process that listens outside a shell, e.g. an exec'd server.
			out = append(out, Shell{PID: pid, Agent: lp.agent, Command: lp.p.cmd, Kind: "server", Started: lp.p.started, Procs: 1})
			i = len(out) - 1
			owner[pid] = i
		}
		out[i].Ports = append(out[i].Ports, ps...)
	}
	var tops []int
	for i := range out {
		tops = append(tops, out[i].PID)
	}
	files := openFiles(ctx, tops)
	for i := range out {
		sort.Ints(out[i].Ports)
		out[i].Ports = uniq(out[i].Ports)
		f := files[out[i].PID]
		out[i].Cwd, out[i].Out = f.cwd, f.out
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out, nil
}

type looseProc struct {
	p     *proc
	agent string
}

func uniq(xs []int) []int {
	var out []int
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// list runs ps for every process.
func list(ctx context.Context) ([]*proc, error) {
	out, err := exec.CommandContext(ctx, "ps", "-A", "-ww", "-o", "pid=,ppid=,etime=,command=").Output()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var ps []*proc
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		el, err3 := ParseEtime(f[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		line := strings.TrimLeft(sc.Text(), " ")
		for range 3 { // drop pid, ppid, etime, keep the command as written
			line = strings.TrimLeft(line[strings.IndexByte(line, ' '):], " ")
		}
		ps = append(ps, &proc{pid: pid, ppid: ppid, started: now.Add(-el), cmd: line})
	}
	return ps, sc.Err()
}

// ParseEtime parses ps's elapsed time, [[dd-]hh:]mm:ss.
func ParseEtime(s string) (time.Duration, error) {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, err
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, errors.New("bad etime " + s)
	}
	var total int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, err
		}
		total = total*60 + n
	}
	return time.Duration(days*86400+total) * time.Second, nil
}

// shellCommand reports whether cmd is a shell running a command, and which.
// Claude Code runs `zsh -c 'source <snapshot> … && eval '<command>' < /dev/null
// && pwd -P >| /tmp/claude-…-cwd'`; the command is the eval'd word.
func shellCommand(cmd string) (string, bool) {
	f := strings.Fields(cmd)
	if len(f) < 3 {
		return "", false
	}
	base := f[0][strings.LastIndexByte(f[0], '/')+1:]
	switch strings.TrimPrefix(base, "-") {
	case "sh", "bash", "zsh", "dash", "fish":
	default:
		return "", false
	}
	i := strings.Index(cmd, " -c ")
	if i < 0 {
		return "", false
	}
	body := strings.TrimSpace(cmd[i+4:])
	body = strings.TrimPrefix(body, "-l ")
	if j := strings.Index(body, "&& eval "); j >= 0 {
		if w, ok := shellWord(body[j+8:]); ok {
			return strings.TrimSpace(w), true
		}
	}
	if body[0] == '\'' || body[0] == '"' {
		if w, ok := shellWord(body); ok {
			return strings.TrimSpace(w), true
		}
	}
	return body, true
}

// shellWord reads one shell word at the start of s: '…', "…" and bare runs,
// concatenated. ps shows newlines as \012.
func shellWord(s string) (string, bool) {
	s = strings.ReplaceAll(s, `\012`, "\n")
	var b strings.Builder
	i := 0
	for i < len(s) {
		switch ch := s[i]; {
		case ch == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return "", false
			}
			b.WriteString(s[i+1 : i+1+j])
			i += j + 2
		case ch == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				b.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return "", false
			}
			i = j + 1
		case ch == ' ' || ch == '\t':
			return b.String(), b.Len() > 0
		case ch == '\\' && i+1 < len(s):
			b.WriteByte(s[i+1])
			i += 2
		default:
			b.WriteByte(ch)
			i++
		}
	}
	return b.String(), b.Len() > 0
}

// listening maps pids to the TCP ports they listen on.
func listening(ctx context.Context) map[int][]int {
	out, _ := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpn").Output()
	ports := map[int][]int{}
	pid := 0
	for _, l := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(l, "p"):
			pid, _ = strconv.Atoi(l[1:])
		case strings.HasPrefix(l, "n"):
			if i := strings.LastIndexByte(l, ':'); i >= 0 {
				if p, err := strconv.Atoi(l[i+1:]); err == nil {
					ports[pid] = append(ports[pid], p)
				}
			}
		}
	}
	return ports
}

type files struct{ cwd, out string }

// openFiles reads the working directory and, if it's a file, the stdout of pids.
func openFiles(ctx context.Context, pids []int) map[int]files {
	res := map[int]files{}
	if len(pids) == 0 {
		return res
	}
	var ids []string
	for _, p := range pids {
		ids = append(ids, strconv.Itoa(p))
	}
	out, _ := exec.CommandContext(ctx, "lsof", "-nP", "-a", "-p", strings.Join(ids, ","), "-d", "cwd,1", "-Ffptn").Output()
	pid, fd, typ := 0, "", ""
	for _, l := range strings.Split(string(out), "\n") {
		if l == "" {
			continue
		}
		switch l[0] {
		case 'p':
			pid, _ = strconv.Atoi(l[1:])
		case 'f':
			fd = l[1:]
		case 't':
			typ = l[1:]
		case 'n':
			f := res[pid]
			switch {
			case fd == "cwd":
				f.cwd = l[1:]
			case fd == "1" && typ == "REG":
				f.out = l[1:]
			}
			res[pid] = f
		}
	}
	return res
}

// Tail returns the last max bytes of path, starting at a line.
func Tail(path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	off := st.Size() - max
	if off < 0 {
		off = 0
	}
	b := make([]byte, st.Size()-off)
	n, err := f.ReadAt(b, off)
	if err != nil && n == 0 && len(b) > 0 {
		return "", err
	}
	b = b[:n]
	if off > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return string(b), nil
}

// Stop ends a shell and everything it started: SIGTERM to the tree, children
// first, then SIGKILL to whatever is left after a grace period.
func Stop(ctx context.Context, pid int) error {
	if pid <= 1 || pid == os.Getpid() {
		return errors.New("refusing to signal pid " + strconv.Itoa(pid))
	}
	ps, err := list(ctx)
	if err != nil {
		return err
	}
	kids := map[int][]*proc{}
	var top *proc
	for _, p := range ps {
		kids[p.ppid] = append(kids[p.ppid], p)
		if p.pid == pid {
			top = p
		}
	}
	if top == nil {
		return errors.New("no process " + strconv.Itoa(pid))
	}
	tree := append(descendants(kids, top), top)
	for i := len(tree) - 1; i >= 0; i-- {
		_ = syscall.Kill(tree[i].pid, syscall.SIGTERM)
	}
	go func() {
		time.Sleep(3 * time.Second)
		for _, p := range tree {
			_ = syscall.Kill(p.pid, syscall.SIGKILL)
		}
	}()
	return nil
}

func descendants(kids map[int][]*proc, p *proc) []*proc {
	var out []*proc
	for _, k := range kids[p.pid] {
		out = append(out, k)
		out = append(out, descendants(kids, k)...)
	}
	return out
}
