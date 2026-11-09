package tui

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Commit is a commit of the current run: a worker's subtask, a merge into the run
// branch, or the run landing on your branch.
type Commit struct {
	SHA, Author, Subject, Refs string
	At                         time.Time
	Files, Add, Del            int
	Sub                        string // the subtask, from the subject ("halo: S2 …")
}

// Short is the abbreviated SHA.
func (c Commit) Short() string {
	if len(c.SHA) > 7 {
		return c.SHA[:7]
	}
	return c.SHA
}

var subRe = regexp.MustCompile(`\b(S\d{1,2})\b`)

// SetCommits merges commits into the run's list, oldest first. Commits seen once
// stay, so the list survives the run's branches being deleted after it lands.
func (s *State) SetCommits(cs []Commit) {
	seen := map[string]bool{}
	for _, c := range s.Commits {
		seen[c.SHA] = true
	}
	added := false
	for _, c := range cs {
		if !seen[c.SHA] {
			s.Commits, seen[c.SHA], added = append(s.Commits, c), true, true
		}
	}
	if added {
		sort.SliceStable(s.Commits, func(i, j int) bool { return s.Commits[i].At.Before(s.Commits[j].At) })
	}
}

// RunShort is the run's id as Halo's branches spell it: halo/<short>.
func RunShort(root string) string {
	id := strings.ReplaceAll(root, "-", "")
	if len(id) > 6 {
		id = id[len(id)-6:]
	}
	return id
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

const logFormat = "--format=%x1e%H%x1f%ct%x1f%an%x1f%D%x1f%s"

// GitCommits lists a run's commits in repo: everything on its branches
// (halo/<short> and halo/<short>-*), plus what Halo committed on the checked-out
// branch since the run started (the merge that lands it).
func GitCommits(ctx context.Context, repo, short string, since time.Time) ([]Commit, error) {
	if repo == "" || short == "" {
		return nil, nil
	}
	// Commit dates count from the prompt: what the run branch inherited is older.
	after := "--since=" + strconv.FormatInt(since.Unix(), 10)
	// A glob without wildcards means "prefix/*" to git, so the run branch's last
	// character goes in brackets to name exactly that branch.
	run := "--glob=refs/heads/halo/" + short[:len(short)-1] + "[" + short[len(short)-1:] + "]"
	subs := "--glob=refs/heads/halo/" + short + "-*"
	out, err := gitOut(ctx, repo, "log", logFormat, "--shortstat", "--diff-merges=first-parent", after, run, subs)
	if err != nil {
		return nil, err
	}
	all := parseLog(out)
	// After the run lands, its branches are gone: find the merge that landed it on
	// the checked-out branch and list what that merge brought in.
	land, err := gitOut(ctx, repo, "log", "-1", "--format=%H", "-i", "--fixed-strings", "--grep=merge halo/"+short, after, "HEAD")
	if land = strings.TrimSpace(land); err == nil && land != "" {
		if out, err := gitOut(ctx, repo, "log", logFormat, "--shortstat", "--diff-merges=first-parent", land+"^1.."+land); err == nil {
			all = append(all, parseLog(out)...)
		}
	}
	return all, nil
}

var statRe = regexp.MustCompile(`(\d+) (file|insertion|deletion)`)

func parseLog(out string) []Commit {
	var cs []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		head, stat, _ := strings.Cut(rec, "\n")
		f := strings.Split(head, "\x1f")
		if len(f) < 5 {
			continue
		}
		secs, _ := strconv.ParseInt(f[1], 10, 64)
		c := Commit{SHA: f[0], At: time.Unix(secs, 0), Author: f[2], Refs: f[3], Subject: f[4]}
		if m := subRe.FindStringSubmatch(c.Subject); m != nil {
			c.Sub = m[1]
		}
		for _, m := range statRe.FindAllStringSubmatch(stat, -1) {
			n, _ := strconv.Atoi(m[1])
			switch m[2] {
			case "file":
				c.Files = n
			case "insertion":
				c.Add = n
			case "deletion":
				c.Del = n
			}
		}
		cs = append(cs, c)
	}
	return cs
}

// GitShow returns a commit's message, stat and patch (against its first parent).
func GitShow(ctx context.Context, repo, sha string) (string, error) {
	out, err := gitOut(ctx, repo, "show", "--stat", "--patch", "--diff-merges=first-parent", "--format=commit %H%nAuthor: %an%nDate:   %cr%n%n    %s%n", sha)
	if err != nil {
		return "", err
	}
	if len(out) > 400_000 {
		out = out[:400_000] + "\n… (cut at 400 KB)"
	}
	return out, nil
}
