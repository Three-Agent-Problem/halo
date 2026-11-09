package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// git runs git in dir and returns its trimmed output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return strings.TrimSpace(out.String()), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// worktrees creates and merges the git worktrees of runs, outside the repository so
// that the user's checkout never changes.
type worktrees struct {
	repo string
	root string // ~/Library/Caches/halo/<repo>-<hash>
}

func newWorktrees(repo string) (*worktrees, error) {
	if _, err := git(context.Background(), repo, "rev-parse", "--verify", "HEAD"); err != nil {
		return nil, fmt.Errorf("%s needs to be a git repository with at least one commit: %w", repo, err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(repo))
	return &worktrees{repo: repo, root: filepath.Join(cache, "halo", filepath.Base(repo)+"-"+hex.EncodeToString(sum[:4]))}, nil
}

// runBranch creates the run's integration branch at HEAD and its worktree.
func (w *worktrees) runBranch(ctx context.Context, short string) (branch, dir string, err error) {
	branch = "halo/" + short
	dir = filepath.Join(w.root, short, "_run")
	if _, err := os.Stat(dir); err == nil {
		return branch, dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", "", err
	}
	_, err = git(ctx, w.repo, "worktree", "add", "-b", branch, dir, "HEAD")
	return branch, dir, err
}

// subtask creates a worktree for one attempt at a subtask, branched from the run's
// branch as it is now (so it has every dependency that was merged).
func (w *worktrees) subtask(ctx context.Context, short, runBranch, id string, attempt int) (branch, dir string, err error) {
	name := id
	if attempt > 1 {
		name = fmt.Sprintf("%s-%d", id, attempt)
	}
	branch = runBranch + "-" + name
	dir = filepath.Join(w.root, short, name)
	if _, err := os.Stat(dir); err == nil {
		return branch, dir, nil
	}
	_, err = git(ctx, w.repo, "worktree", "add", "-b", branch, dir, runBranch)
	return branch, dir, err
}

// commitAll commits everything in dir, if anything changed. It returns the short
// stat of the commit, or "" when there was nothing to commit.
func commitAll(ctx context.Context, dir, message string) (string, error) {
	if _, err := git(ctx, dir, "add", "-A"); err != nil {
		return "", err
	}
	if out, _ := git(ctx, dir, "status", "--porcelain"); out == "" {
		return "", nil
	}
	if _, err := git(ctx, dir, "-c", "user.name=halo", "-c", "user.email=halo@localhost", "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	return git(ctx, dir, "show", "--stat", "--format=", "HEAD")
}

// diff returns what branch adds on top of base, capped at n bytes.
func diff(ctx context.Context, repo, base, branch string, n int) string {
	stat, _ := git(ctx, repo, "diff", "--stat", base+"..."+branch)
	patch, _ := git(ctx, repo, "diff", base+"..."+branch)
	if len(patch) > n {
		patch = patch[:n] + "\n… (diff cut at " + fmt.Sprint(n) + " bytes; read the files for the rest)"
	}
	if stat == "" {
		return "(no changes)"
	}
	return stat + "\n\n" + patch
}

// merge merges branch into the run's worktree. On a conflict it aborts and returns
// the conflicting files.
func merge(ctx context.Context, runDir, branch, message string) (conflicts string, err error) {
	_, err = git(ctx, runDir, "-c", "user.name=halo", "-c", "user.email=halo@localhost", "merge", "--no-ff", "-m", message, branch)
	if err == nil {
		return "", nil
	}
	files, _ := git(ctx, runDir, "diff", "--name-only", "--diff-filter=U")
	_, _ = git(ctx, runDir, "merge", "--abort")
	if files == "" {
		return "", err
	}
	return files, nil
}
