package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), dir, append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, name, content string) {
	t.Helper()
	write(t, dir, name, content)
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "add "+name)
}

func newRepo(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "init", "-q", "-b", "main")
	commit(t, repo, "a.txt", "a\n")
	return repo
}

// runWorktree makes halo/<short> with one commit adding name.
func runWorktree(t *testing.T, repo, short, name, content string) string {
	t.Helper()
	w, err := newWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	branch, dir, err := w.runBranch(context.Background(), short)
	if err != nil {
		t.Fatal(err)
	}
	commit(t, dir, name, content)
	return branch
}

func TestLandMergesIntoCheckout(t *testing.T) {
	repo := newRepo(t)
	branch := runWorktree(t, repo, "r1", "new.txt", "hi\n")
	write(t, repo, "untracked.txt", "x")
	w, _ := newWorktrees(repo)
	if b, err := w.base(context.Background()); err != nil || b != "main" {
		t.Fatalf("base = %q, %v", b, err)
	}
	sum, err := Land(context.Background(), repo, branch, "main")
	if err != nil {
		t.Fatal(err)
	}
	if sum == "" {
		t.Error("empty summary")
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err != nil {
		t.Error("file missing in checkout")
	}
}

func TestLandRefusesTrackedChanges(t *testing.T) {
	repo := newRepo(t)
	branch := runWorktree(t, repo, "r1", "new.txt", "hi\n")
	write(t, repo, "a.txt", "changed\n")
	if _, err := Land(context.Background(), repo, branch, "main"); err == nil {
		t.Fatal("expected refusal")
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err == nil {
		t.Error("merge happened")
	}
	gitT(t, repo, "checkout", "-q", "--", "a.txt")
	write(t, repo, "untracked.txt", "x")
	if _, err := Land(context.Background(), repo, branch, "main"); err != nil {
		t.Fatal(err)
	}
}

func TestLandConflictAborts(t *testing.T) {
	repo := newRepo(t)
	branch := runWorktree(t, repo, "r1", "a.txt", "run\n")
	commit(t, repo, "a.txt", "main\n")
	_, err := Land(context.Background(), repo, branch, "main")
	if err == nil || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("err = %v", err)
	}
	if out := gitT(t, repo, "status", "--porcelain"); out != "" {
		t.Errorf("repo not clean: %s", out)
	}
	if _, err := git(context.Background(), repo, "rev-parse", "--verify", "MERGE_HEAD"); err == nil {
		t.Error("still merging")
	}
}

func TestLandNotCheckedOut(t *testing.T) {
	repo := newRepo(t)
	gitT(t, repo, "branch", "dev")
	branch := runWorktree(t, repo, "r1", "new.txt", "hi\n")
	if _, err := Land(context.Background(), repo, branch, "dev"); err != nil {
		t.Fatal(err)
	}
	if gitT(t, repo, "rev-parse", "dev") != gitT(t, repo, "rev-parse", branch) {
		t.Error("dev not fast-forwarded")
	}
	// A second run's branch and dev2 each have a commit the other lacks.
	gitT(t, repo, "branch", "dev2", "main")
	branch2 := runWorktree(t, repo, "r2", "other.txt", "o\n")
	gitT(t, repo, "checkout", "-q", "dev2")
	commit(t, repo, "m.txt", "m\n")
	gitT(t, repo, "checkout", "-q", "main")
	if _, err := Land(context.Background(), repo, branch2, "dev2"); err == nil {
		t.Fatal("expected non-ff refusal")
	}
}

func TestCleanup(t *testing.T) {
	repo := newRepo(t)
	runWorktree(t, repo, "r1", "new.txt", "hi\n")
	w, _ := newWorktrees(repo)
	if _, _, err := w.subtask(context.Background(), "r1", "halo/r1", "S1", 1); err != nil {
		t.Fatal(err)
	}
	runWorktree(t, repo, "r2", "x.txt", "x\n")
	if err := Cleanup(context.Background(), repo, "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.root, "r1")); err == nil {
		t.Error("dir remains")
	}
	branches := gitT(t, repo, "branch", "--list", "halo/*")
	if strings.Contains(branches, "halo/r1") || !strings.Contains(branches, "halo/r2") {
		t.Errorf("branches: %s", branches)
	}
	if wl := gitT(t, repo, "worktree", "list"); strings.Contains(wl, "/r1/") || !strings.Contains(wl, "/r2/") {
		t.Errorf("worktrees: %s", wl)
	}
}
