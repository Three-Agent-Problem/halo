package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/three-agent-problem/lagrange/client"

	"github.com/three-agent-problem/halo/internal/config"
	"github.com/three-agent-problem/halo/internal/host"
)

// subtaskBranch matches the branches of single subtasks (halo/<run>-S<n> or
// halo/<run>-S<n>-<k>), which are merged by their run, not on their own.
var subtaskBranch = regexp.MustCompile(`-S\d+(-\d+)?$`)

// normalizeRun turns "c1e175" or "halo/c1e175" into "c1e175".
func normalizeRun(arg string) (string, error) {
	run := strings.TrimPrefix(strings.TrimSpace(arg), "halo/")
	if run == "" || strings.Contains(run, "/") {
		return "", fmt.Errorf("invalid run %q: want a run name like c1e175 or halo/c1e175", arg)
	}
	return run, nil
}

// runBranches keeps the run branches (halo/<run>) from names, dropping subtask branches.
func runBranches(names []string) []string {
	var out []string
	for _, name := range names {
		if !strings.HasPrefix(name, "halo/") || subtaskBranch.MatchString(name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// gitOut runs git in dir and returns its trimmed stdout.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func mergeCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	keep := fs.Bool("keep", false, "Keep the run's branches, worktrees and cache after merging.")
	// Accept flags after the run name too (halo merge c1e175 --keep).
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 1 {
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		rest = append(rest[:1], fs.Args()...)
	}

	repo, err := gitOut(ctx, ".", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("halo merge must run inside a git repository: %w", err)
	}

	if len(rest) == 0 {
		return listRuns(ctx, repo)
	}
	if len(rest) > 1 {
		return fmt.Errorf("halo merge takes one run, got %d arguments", len(rest))
	}
	run, err := normalizeRun(rest[0])
	if err != nil {
		return err
	}
	current, err := gitOut(ctx, repo, "symbolic-ref", "--short", "HEAD")
	if err != nil || current == "" {
		return errors.New("HEAD is detached: check out the branch to merge into first")
	}
	summary, err := host.Land(ctx, repo, "halo/"+run, current)
	if err != nil {
		return err
	}
	fmt.Println(summary)
	if md := runSummary(ctx, run); md != "" {
		fmt.Println()
		fmt.Println(md)
	}
	if *keep {
		return nil
	}
	return host.Cleanup(ctx, repo, run)
}

// listRuns prints the run branches of repo with the age of their last commit.
func listRuns(ctx context.Context, repo string) error {
	refs, err := gitOut(ctx, repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/halo/")
	if err != nil {
		return err
	}
	runs := runBranches(strings.Split(refs, "\n"))
	if len(runs) == 0 {
		fmt.Println("no halo runs to merge")
		return nil
	}
	reports := runReports(ctx)
	for _, branch := range runs {
		when, err := gitOut(ctx, repo, "log", "-1", "--format=%cr", branch)
		if err != nil {
			return err
		}
		if h := headlineFor(reports, strings.TrimPrefix(branch, "halo/")); h != "" {
			fmt.Printf("%s\t%s\t%s\n", branch, when, h)
		} else {
			fmt.Printf("%s\t%s\n", branch, when)
		}
	}
	return nil
}

// runReports fetches recent runs from Lagrange. Best effort: nil when there is
// no server or the query fails.
func runReports(ctx context.Context) []*client.Run {
	st, err := config.LoadState()
	if err != nil || st.Server == "" || st.Key == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	runs, err := client.New(st.Server, st.Key).Runs(ctx, client.RunFilter{Limit: 200})
	if err != nil {
		return nil
	}
	return runs
}

// findReport is the run with a report whose ID (dashes removed) ends in short.
func findReport(runs []*client.Run, short string) *client.Run {
	for _, r := range runs {
		if r.Report != nil && strings.HasSuffix(strings.ReplaceAll(r.ID, "-", ""), short) {
			return r
		}
	}
	return nil
}

// headlineFor is the report headline of the run named short, or "".
func headlineFor(runs []*client.Run, short string) string {
	if r := findReport(runs, short); r != nil {
		return strings.TrimSpace(r.Report.Headline)
	}
	return ""
}

// runSummary is the run's situation report from Lagrange as Markdown, or "" when
// there is no server, no such run, or no report. Best effort: merging doesn't need it.
func runSummary(ctx context.Context, short string) string {
	if r := findReport(runReports(ctx), short); r != nil {
		return r.Report.Markdown()
	}
	return ""
}
