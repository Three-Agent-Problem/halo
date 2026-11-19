package plan

import (
	"fmt"
	"strings"
	"testing"
)

func arch(nodes int) Block {
	b := Block{Kind: KindArchitecture, Groups: []Group{{ID: "a", Label: "A"}}}
	for i := range nodes {
		b.Nodes = append(b.Nodes, Node{ID: fmt.Sprint(i), Label: "n", Group: "a"})
	}
	return b
}

func TestValidateBlocks(t *testing.T) {
	ok := []Block{
		{Kind: KindArchitecture, Groups: []Group{{ID: "api", Label: "api"}},
			Nodes: []Node{{ID: "r", Label: "routes", Group: "api", Change: New, Subtask: "S1"}, {ID: "s", Label: "store"}},
			Edges: []Edge{{From: "r", To: "s", Change: New}}},
		{Kind: KindFlow, Steps: []Step{{From: "browser", To: "api", Text: "POST"}, {From: "api", Text: "validate"}}},
		{Kind: KindFiles, Paths: []Path{{Path: "api/tasks.go", Change: New, Subtask: "S1"}}},
		{Kind: KindCompare, Columns: []string{"a", "b"}, Rows: [][]string{{"1", "2"}}},
		{Kind: KindNote, Tone: "question", Text: "keep them?"},
	}
	if err := ValidateBlocks(ok, []string{"S1"}); err != nil {
		t.Fatal(err)
	}
	// Before a plan exists, subtask references aren't checked.
	if err := ValidateBlocks(ok, nil); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]Block{
		"subtask":     ok,
		"too many":    make([]Block, MaxBlocks+1),
		"kind":        {{Kind: "mermaid"}},
		"big":         {arch(MaxNodes + 1)},
		"edge":        {{Kind: KindArchitecture, Nodes: []Node{{ID: "a", Label: "a"}}, Edges: []Edge{{From: "a", To: "zz"}}}},
		"group":       {{Kind: KindArchitecture, Nodes: []Node{{ID: "a", Label: "a", Group: "nope"}}}},
		"change":      {{Kind: KindFiles, Paths: []Path{{Path: "x", Change: "added"}}}},
		"dup node":    {{Kind: KindArchitecture, Nodes: []Node{{ID: "a", Label: "a"}, {ID: "a", Label: "b"}}}},
		"empty note":  {{Kind: KindNote}},
		"tone":        {{Kind: KindNote, Text: "x", Tone: "loud"}},
		"compare row": {{Kind: KindCompare, Columns: []string{"a", "b"}, Rows: [][]string{{"1", "2", "3"}}}},
	}
	for name, bs := range bad {
		subs := []string{"S1"}
		if name == "subtask" {
			subs = []string{"S2"}
		}
		if err := ValidateBlocks(bs, subs); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := ValidateBlocks([]Block{arch(MaxNodes + 1)}, nil); err == nil || !strings.Contains(err.Error(), "only the part") {
		t.Errorf("a big architecture should say how to shrink it: %v", err)
	}
}

func TestDiff(t *testing.T) {
	old := &Plan{Subtasks: []*Subtask{
		{ID: "S1", Title: "a", Model: "haiku", Spec: "x"},
		{ID: "S2", Title: "b", Model: "sonnet", Spec: "y"},
		{ID: "S3", Title: "c", Model: "opus", Spec: "z"},
	}}
	p := &Plan{Subtasks: []*Subtask{
		{ID: "S1", Title: "a", Model: "haiku", Spec: "x", Status: Running}, // status alone isn't a change
		{ID: "S2", Title: "b", Model: "opus", Spec: "y"},
		{ID: "S4", Title: "d", Model: "haiku", Spec: "w"},
	}}
	marks, removed := Diff(old, p)
	if len(marks) != 2 || marks["S2"] != "~" || marks["S4"] != "+" {
		t.Errorf("marks = %v", marks)
	}
	if len(removed) != 1 || removed[0] != "S3" {
		t.Errorf("removed = %v", removed)
	}
	if m, r := Diff(nil, p); len(m) != 0 || r != nil {
		t.Errorf("no old plan: %v %v", m, r)
	}
}
