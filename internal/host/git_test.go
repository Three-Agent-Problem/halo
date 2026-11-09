package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.name", "test")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "base.txt"), "one\ntwo\nthree\n")
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func newTestWorktrees(t *testing.T) *worktrees {
	t.Helper()
	repo := newRepo(t)
	w := &worktrees{repo: repo, root: t.TempDir()}
	t.Cleanup(func() {
		_, _ = git(context.Background(), repo, "worktree", "prune")
		os.RemoveAll(w.root)
		_, _ = git(context.Background(), repo, "worktree", "prune")
	})
	return w
}

func TestNewWorktreesErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if _, err := newWorktrees(t.TempDir()); err == nil {
		t.Error("expected error for non-git dir")
	}
	empty := t.TempDir()
	mustGit(t, empty, "init", "-q", "-b", "main")
	if _, err := newWorktrees(empty); err == nil {
		t.Error("expected error for repo without commits")
	}
}

func TestRunBranch(t *testing.T) {
	w := newTestWorktrees(t)
	ctx := context.Background()
	branch, dir, err := w.runBranch(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "halo/abc123" {
		t.Errorf("branch = %q", branch)
	}
	if want := filepath.Join(w.root, "abc123", "_run"); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("dir missing: %v", err)
	}
	if got := mustGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != branch {
		t.Errorf("checked out %q, want %q", got, branch)
	}
	b2, d2, err := w.runBranch(ctx, "abc123")
	if err != nil || b2 != branch || d2 != dir {
		t.Errorf("second call = %q, %q, %v", b2, d2, err)
	}
}

func TestSubtask(t *testing.T) {
	w := newTestWorktrees(t)
	ctx := context.Background()
	runBranch, runDir, err := w.runBranch(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(runDir, "dep.txt"), "dep\n")
	if _, err := commitAll(ctx, runDir, "dep"); err != nil {
		t.Fatal(err)
	}

	b1, d1, err := w.subtask(ctx, "abc123", runBranch, "S1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if b1 != "halo/abc123-S1" {
		t.Errorf("branch = %q", b1)
	}
	if want := filepath.Join(w.root, "abc123", "S1"); d1 != want {
		t.Errorf("dir = %q, want %q", d1, want)
	}
	if _, err := os.Stat(filepath.Join(d1, "dep.txt")); err != nil {
		t.Errorf("subtask worktree lacks run branch commit: %v", err)
	}
	if got := mustGit(t, d1, "rev-parse", "--abbrev-ref", "HEAD"); got != b1 {
		t.Errorf("checked out %q, want %q", got, b1)
	}

	b2, d2, err := w.subtask(ctx, "abc123", runBranch, "S1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if b2 != "halo/abc123-S1-2" {
		t.Errorf("branch = %q", b2)
	}
	if want := filepath.Join(w.root, "abc123", "S1-2"); d2 != want {
		t.Errorf("dir = %q, want %q", d2, want)
	}
}

func TestCommitAll(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	head := mustGit(t, repo, "rev-parse", "HEAD")

	stat, err := commitAll(ctx, repo, "nothing")
	if err != nil || stat != "" {
		t.Fatalf("no changes: %q, %v", stat, err)
	}
	if got := mustGit(t, repo, "rev-parse", "HEAD"); got != head {
		t.Error("HEAD changed without changes")
	}

	writeFile(t, filepath.Join(repo, "new.txt"), "new\n")
	writeFile(t, filepath.Join(repo, "base.txt"), "changed\n")
	stat, err = commitAll(ctx, repo, "my message")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stat, "new.txt") || !strings.Contains(stat, "base.txt") {
		t.Errorf("stat = %q", stat)
	}
	if got := mustGit(t, repo, "log", "-1", "--format=%s"); got != "my message" {
		t.Errorf("message = %q", got)
	}
	if got := mustGit(t, repo, "log", "-1", "--format=%an"); got != "halo" {
		t.Errorf("author = %q", got)
	}
}

func TestDiff(t *testing.T) {
	w := newTestWorktrees(t)
	ctx := context.Background()
	runBranch, _, err := w.runBranch(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	branch, dir, err := w.subtask(ctx, "abc123", runBranch, "S1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := diff(ctx, w.repo, runBranch, branch, 10000); got != "(no changes)" {
		t.Errorf("got %q", got)
	}
	writeFile(t, filepath.Join(dir, "feature.txt"), strings.Repeat("line of content\n", 50))
	if _, err := commitAll(ctx, dir, "feature"); err != nil {
		t.Fatal(err)
	}
	got := diff(ctx, w.repo, runBranch, branch, 100000)
	if !strings.Contains(got, "feature.txt") || !strings.Contains(got, "+line of content") {
		t.Errorf("diff = %q", got)
	}
	if strings.Contains(got, "diff cut at") {
		t.Error("unexpected cut")
	}
	cut := diff(ctx, w.repo, runBranch, branch, 20)
	if !strings.Contains(cut, "diff cut at 20") {
		t.Errorf("cut = %q", cut)
	}
}

func TestMerge(t *testing.T) {
	w := newTestWorktrees(t)
	ctx := context.Background()
	runBranch, runDir, err := w.runBranch(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}

	// Clean merge.
	b1, d1, err := w.subtask(ctx, "abc123", runBranch, "S1", 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d1, "s1.txt"), "s1\n")
	if _, err := commitAll(ctx, d1, "s1"); err != nil {
		t.Fatal(err)
	}
	conflicts, err := merge(ctx, runDir, b1, "merge S1")
	if err != nil || conflicts != "" {
		t.Fatalf("clean merge: %q, %v", conflicts, err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "s1.txt")); err != nil {
		t.Errorf("merged file missing: %v", err)
	}

	// Conflicting merge.
	b2, d2, err := w.subtask(ctx, "abc123", runBranch, "S2", 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d2, "base.txt"), "one\nfrom subtask\nthree\n")
	if _, err := commitAll(ctx, d2, "s2"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(runDir, "base.txt"), "one\nfrom run\nthree\n")
	if _, err := commitAll(ctx, runDir, "run edit"); err != nil {
		t.Fatal(err)
	}
	conflicts, err = merge(ctx, runDir, b2, "merge S2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conflicts, "base.txt") {
		t.Errorf("conflicts = %q", conflicts)
	}
	if out := mustGit(t, runDir, "status", "--porcelain"); out != "" {
		t.Errorf("run worktree dirty: %q", out)
	}
	if _, err := git(ctx, runDir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		t.Error("MERGE_HEAD still present")
	}
}
