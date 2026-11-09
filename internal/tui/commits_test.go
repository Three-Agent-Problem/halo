package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	who := "ada"
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=" + who, "-c", "user.email=h@x", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.txt", "a\n")
	git("add", "-A")
	git("commit", "-qm", "base")
	time.Sleep(1100 * time.Millisecond) // commit dates have one-second resolution
	start := time.Now()
	who = "halo"
	git("checkout", "-qb", "halo/abc123")
	git("checkout", "-qb", "halo/abc123-S1")
	write("b.txt", "one\ntwo\n")
	git("add", "-A")
	git("commit", "-qm", "halo: S1 add b")
	git("checkout", "-q", "halo/abc123")
	git("merge", "-q", "--no-ff", "-m", "halo: merge S1 add b", "halo/abc123-S1")
	git("checkout", "-q", "main")
	git("merge", "-q", "--no-ff", "-m", "halo: merge halo/abc123", "halo/abc123")
	git("branch", "-q", "-D", "halo/abc123", "halo/abc123-S1") // cleaned up after landing
	git("checkout", "-qb", "halo/other-S1")                    // another run: not listed
	write("c.txt", "c\n")
	git("add", "-A")
	git("commit", "-qm", "halo: S1 other run")

	cs, err := GitCommits(context.Background(), repo, "abc123", start)
	if err != nil {
		t.Fatal(err)
	}
	st := &State{}
	st.SetCommits(cs)
	st.SetCommits(cs) // idempotent
	var subjects []string
	for _, c := range st.Commits {
		subjects = append(subjects, c.Subject)
	}
	if len(subjects) != 3 || !strings.Contains(strings.Join(subjects, " | "), "halo: merge halo/abc123") {
		t.Fatalf("commits: %s", strings.Join(subjects, " | "))
	}
	for _, c := range st.Commits {
		if c.Subject == "halo: merge halo/abc123" {
			continue
		}
		if c.Sub != "S1" || c.Files != 1 || c.Add != 2 {
			t.Errorf("%s: sub %q files %d +%d", c.Subject, c.Sub, c.Files, c.Add)
		}
	}
	out, err := GitShow(context.Background(), repo, st.Commits[0].SHA)
	if err != nil || !strings.Contains(out, "+one") {
		t.Fatalf("show: %v\n%s", err, out)
	}
	if RunShort("01a1c114-847f-7ae1-9847-b927e9c1e175") != "c1e175" {
		t.Fatal("RunShort")
	}
}
