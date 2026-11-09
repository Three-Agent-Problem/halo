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
	root, err := cacheRoot(repo)
	if err != nil {
		return nil, err
	}
	return &worktrees{repo: repo, root: root}, nil
}

// cacheRoot is where the worktrees of repo's runs live.
func cacheRoot(repo string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(cache, "halo", filepath.Base(repo)+"-"+hex.EncodeToString(sum[:4])), nil
}

// RunDir is the worktree of the run branch for a run's short id, the directory
// runBranch creates, so that the TUI can find where the run's code lives.
func RunDir(repo, short string) (string, error) {
	root, err := cacheRoot(repo)
	if err != nil {
		return "", err
	}
	return runDirIn(root, short), nil
}

func runDirIn(root, short string) string { return filepath.Join(root, short, "_run") }

// base returns the branch checked out in the repository, which is what a run's
// branch gets merged into. It fails on a detached HEAD.
func (w *worktrees) base(ctx context.Context) (string, error) {
	b, err := git(ctx, w.repo, "symbolic-ref", "--short", "HEAD")
	if err != nil || b == "" {
		return "", fmt.Errorf("%s has a detached HEAD; check out a branch first: %w", w.repo, err)
	}
	return b, nil
}

// runBranch creates the run's integration branch at HEAD and its worktree.
func (w *worktrees) runBranch(ctx context.Context, short string) (branch, dir string, err error) {
	branch = "halo/" + short
	dir = runDirIn(w.root, short)
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

// Land merges the run's branch into base without leaving the user's checkout in a
// broken state. If base is checked out in repo, it makes a merge commit there (and
// refuses when tracked files have uncommitted changes); otherwise it only
// fast-forwards base. It returns a short summary of what was merged.
func Land(ctx context.Context, repo, runBranch, base string) (string, error) {
	current, _ := git(ctx, repo, "symbolic-ref", "--short", "HEAD")
	if current != base {
		if _, err := git(ctx, repo, "merge-base", "--is-ancestor", base, runBranch); err != nil {
			return "", fmt.Errorf("%s is not checked out and cannot be fast-forwarded to %s; merge %s by hand", base, runBranch, runBranch)
		}
		old, err := git(ctx, repo, "rev-parse", "--short", base)
		if err != nil {
			return "", err
		}
		if _, err := git(ctx, repo, "update-ref", "refs/heads/"+base, runBranch); err != nil {
			return "", err
		}
		stat, _ := git(ctx, repo, "diff", "--shortstat", old, base)
		return fmt.Sprintf("fast-forwarded %s %s..%s: %s", base, old, shortRev(ctx, repo, runBranch), stat), nil
	}

	if dirty, err := git(ctx, repo, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return "", err
	} else if dirty != "" {
		return "", fmt.Errorf("%s has uncommitted changes to tracked files; commit or stash them, then land %s:\n%s", repo, runBranch, dirty)
	}
	before, err := git(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	_, err = git(ctx, repo, "-c", "user.name=halo", "-c", "user.email=halo@localhost", "merge", "--no-ff", "-m", "halo: merge "+runBranch, runBranch)
	if err != nil {
		files, _ := git(ctx, repo, "diff", "--name-only", "--diff-filter=U")
		_, _ = git(ctx, repo, "merge", "--abort")
		if files != "" {
			return "", fmt.Errorf("merging %s into %s conflicts in: %s (merge aborted, %s is unchanged)", runBranch, base, strings.ReplaceAll(files, "\n", ", "), base)
		}
		return "", err
	}
	after, err := git(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if after == before {
		return base + " already contains " + runBranch, nil
	}
	stat, _ := git(ctx, repo, "diff", "--shortstat", before, after)
	return fmt.Sprintf("merged %s into %s as %s: %s", runBranch, base, shortRev(ctx, repo, "HEAD"), stat), nil
}

func shortRev(ctx context.Context, repo, rev string) string {
	out, err := git(ctx, repo, "rev-parse", "--short", rev)
	if err != nil {
		return rev
	}
	return out
}

// Cleanup removes what run short left behind: its worktrees, its branches
// (halo/<short> and halo/<short>-*) and its cache directory. Call it only after Land
// succeeded.
func Cleanup(ctx context.Context, repo, short string) error {
	if short == "" || strings.ContainsAny(short, `/\`) || short == "." || short == ".." {
		return fmt.Errorf("invalid run id %q", short)
	}
	root, err := cacheRoot(repo)
	if err != nil {
		return err
	}
	runDir := filepath.Join(root, short)
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	prefix := resolve(runDir) + string(filepath.Separator)

	list, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	var errs []string
	for _, line := range strings.Split(list, "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok || !strings.HasPrefix(resolve(path)+string(filepath.Separator), prefix) {
			continue
		}
		if _, err := git(ctx, repo, "worktree", "remove", "--force", path); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if _, err := git(ctx, repo, "worktree", "prune"); err != nil {
		errs = append(errs, err.Error())
	}
	branches, err := git(ctx, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/halo/"+short, "refs/heads/halo/"+short+"-*")
	if err != nil {
		return err
	}
	for _, b := range strings.Fields(branches) {
		if _, err := git(ctx, repo, "branch", "-D", b); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := os.RemoveAll(runDir); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("cleanup of run %s: %s", short, strings.Join(errs, "; "))
	}
	return nil
}
