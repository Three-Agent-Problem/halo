package tui

import (
	"strings"
	"testing"
	"time"
)

// plainGraph draws the rows without colors, each with its commit's subject.
func plainGraph(cs []Commit, rows []grow) []string {
	var out []string
	w := graphWidth(rows)
	for i, r := range rows {
		out = append(out, strings.TrimRight(stripANSI(renderCells(r.cells, w))+" "+cs[i].Subject, " "))
	}
	return out
}

// history builds commits from "sha parent,parent subject" lines, oldest first,
// one second apart.
func history(lines ...string) []Commit {
	t0 := time.Unix(1_700_000_000, 0)
	var cs []Commit
	for i, l := range lines {
		f := strings.SplitN(l, " ", 3)
		var ps []string
		if f[1] != "-" {
			ps = strings.Split(f[1], ",")
		}
		cs = append(cs, Commit{SHA: f[0], Parents: ps, Subject: f[2], At: t0.Add(time.Duration(i) * time.Second)})
	}
	return cs
}

func checkGraph(t *testing.T, cs []Commit, want ...string) {
	t.Helper()
	cs = orderCommits(cs)
	got := plainGraph(cs, graph(cs, func(Commit) string { return "fg" }))
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("graph:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestGraphLinear(t *testing.T) {
	checkGraph(t, history("a base one", "b a two", "c b three"),
		"●  one",
		"●  two",
		"●  three")
}

func TestGraphForkMerge(t *testing.T) {
	// A run like the demo: S1 merged, S2 and S3 in parallel, S3 merged first,
	// then S2, then the run lands on main.
	cs := history(
		"s1 base S1",
		"m1 base,s1 merge S1",
		"s2 m1 S2",
		"s3 m1 S3",
		"m3 m1,s3 merge S3",
		"m2 m3,s2 merge S2",
		"land base,m2 merge halo/run",
	)
	checkGraph(t, cs,
		"●      S1",
		"◆─╮─╮  merge S1",
		"│ ● │  S2",
		"│ │ ●  S3",
		"◆─┼─╯  merge S3",
		"◆─╯    merge S2",
		"◆      merge halo/run",
	)
}

func TestGraphMissingParent(t *testing.T) {
	// A merge whose second parent isn't in the run's commits draws no lane.
	checkGraph(t, history("a base one", "b a,zzz merge from outside"),
		"●  one",
		"◆  merge from outside")
}

func TestOrderCommitsSameSecond(t *testing.T) {
	cs := history("a - one", "b a two", "c b three")
	for i := range cs {
		cs[i].At = cs[0].At
	}
	cs[0], cs[2] = cs[2], cs[0] // listed newest first, all in one second
	got := orderCommits(cs)
	if got[0].SHA != "a" || got[1].SHA != "b" || got[2].SHA != "c" {
		t.Fatalf("parents first: %s %s %s", got[0].SHA, got[1].SHA, got[2].SHA)
	}
}

func TestGraphReviewRound(t *testing.T) {
	// S2 gets a second commit (review changes) after S3 is merged.
	checkGraph(t, history(
		"s1 base S1",
		"m1 base,s1 merge S1",
		"s2 m1 S2",
		"s3 m1 S3",
		"m3 m1,s3 merge S3",
		"s2b s2 S2 changes",
		"m2 m3,s2b merge S2",
	),
		"●      S1",
		"◆─╮─╮  merge S1",
		"│ ● │  S2",
		"│ │ ●  S3",
		"◆─┼─╯  merge S3",
		"│ ●    S2 changes",
		"◆─╯    merge S2",
	)
}

func TestGraphOctopus(t *testing.T) {
	checkGraph(t, history("a - base", "b a B", "c a C", "m a,b,c merge both"),
		"●─╮─╮  base",
		"│ ● │  B",
		"│ │ ●  C",
		"◆─╯─╯  merge both",
	)
}
