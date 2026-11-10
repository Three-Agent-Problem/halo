package main

import (
	"reflect"
	"testing"

	"github.com/three-agent-problem/lagrange/client"
	"github.com/three-agent-problem/lagrange/report"
)

func TestNormalizeRun(t *testing.T) {
	cases := map[string]string{
		"c1e175":      "c1e175",
		"halo/c1e175": "c1e175",
	}
	for in, want := range cases {
		got, err := normalizeRun(in)
		if err != nil || got != want {
			t.Errorf("normalizeRun(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "halo/", "a/b", "halo/x/y"} {
		if _, err := normalizeRun(bad); err == nil {
			t.Errorf("normalizeRun(%q) succeeded; want error", bad)
		}
	}
}

func TestRunBranches(t *testing.T) {
	in := []string{
		"halo/c1e175",
		"halo/ccde88-S3",
		"halo/ccde88-S3-2",
		"halo/abc-S",
		"main",
		"halo/9f0a1b",
	}
	want := []string{"halo/c1e175", "halo/abc-S", "halo/9f0a1b"}
	if got := runBranches(in); !reflect.DeepEqual(got, want) {
		t.Errorf("runBranches = %v; want %v", got, want)
	}
}

func TestHeadlineFor(t *testing.T) {
	rep := func(h string) *report.Report { return &report.Report{Headline: h} }
	runs := []*client.Run{
		{ID: "aaaa-6e75e8", Report: rep("  shipped it \n")},
		{ID: "bbbb-c1e175"},
		{ID: "cc-dd-ee-f00d11", Report: rep("dashed")},
		{ID: "ffff-abcdef", Report: rep("   ")},
	}
	cases := []struct{ short, want string }{
		{"6e75e8", "shipped it"},
		{"c1e175", ""},
		{"nope00", ""},
		{"f00d11", "dashed"},
		{"abcdef", ""},
	}
	for _, c := range cases {
		if got := headlineFor(runs, c.short); got != c.want {
			t.Errorf("headlineFor(%q) = %q; want %q", c.short, got, c.want)
		}
	}
	if got := headlineFor(nil, "6e75e8"); got != "" {
		t.Errorf("headlineFor(nil) = %q", got)
	}
}
