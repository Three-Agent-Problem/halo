package main

import (
	"reflect"
	"testing"
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
