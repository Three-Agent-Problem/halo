package plan

import (
	"strings"
	"testing"
)

func sub(id, model string, deps ...string) *Subtask {
	return &Subtask{ID: id, Title: "t " + id, Spec: "do " + id, Model: model, Deps: deps, Status: Pending}
}

func TestValidate(t *testing.T) {
	models := []string{"haiku", "sonnet", "opus"}
	ok := &Plan{Goal: "g", Subtasks: []*Subtask{sub("S1", "haiku"), sub("S2", "sonnet", "S1"), sub("S3", "opus", "S1", "S2")}}
	if err := ok.Validate(models); err != nil {
		t.Fatalf("valid plan: %v", err)
	}
	for name, tc := range map[string]struct {
		p    *Plan
		want string
	}{
		"no goal":       {&Plan{Subtasks: []*Subtask{sub("S1", "haiku")}}, "goal"},
		"duplicate":     {&Plan{Goal: "g", Subtasks: []*Subtask{sub("S1", "haiku"), sub("s1", "haiku")}}, "duplicate"},
		"unknown model": {&Plan{Goal: "g", Subtasks: []*Subtask{sub("S1", "gpt")}}, "model"},
		"unknown dep":   {&Plan{Goal: "g", Subtasks: []*Subtask{sub("S1", "haiku", "S9")}}, "unknown"},
		"cycle":         {&Plan{Goal: "g", Subtasks: []*Subtask{sub("S1", "haiku", "S2"), sub("S2", "haiku", "S1")}}, "cycle"},
	} {
		if err := tc.p.Validate(models); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want an error about %q", name, err, tc.want)
		}
	}
}

func TestDepsDone(t *testing.T) {
	p := &Plan{Goal: "g", Subtasks: []*Subtask{sub("S1", "haiku"), sub("S2", "sonnet", "S1")}}
	if p.DepsDone(p.Get("S2")) {
		t.Fatal("S2 ready before S1 is done")
	}
	p.Get("S1").Status = Done
	if !p.DepsDone(p.Get("S2")) {
		t.Fatal("S2 not ready after S1 is done")
	}
}

func TestLadderAndTags(t *testing.T) {
	if n, ok := Next("haiku"); !ok || n != "sonnet" {
		t.Fatalf("Next(haiku) = %q %v", n, ok)
	}
	if _, ok := Next("opus"); ok {
		t.Fatal("opus has no next model")
	}
	for in, want := range map[string]string{"[S3] routes": "S3", "↺ [S2] fix": "S2", "no tag": "", "[toolongforanidhere] x": ""} {
		if got := ParseTag(in); got != want {
			t.Errorf("ParseTag(%q) = %q, want %q", in, got, want)
		}
	}
	p := &Plan{Goal: "g", Subtasks: []*Subtask{{ID: "S1", Spec: "secret", Acceptance: "tests"}}}
	if pub := p.Public(); pub.Subtasks[0].Spec != "" || p.Subtasks[0].Spec != "secret" {
		t.Fatal("Public must drop specs without changing the plan")
	}
}
