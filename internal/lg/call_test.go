package lg

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func msg(texts ...string) *a2a.Message {
	var parts []*a2a.Part
	for _, s := range texts {
		parts = append(parts, a2a.NewTextPart(s))
	}
	return a2a.NewMessage(a2a.MessageRoleAgent, parts...)
}

func art(name string, texts ...string) *a2a.Artifact {
	a := &a2a.Artifact{Name: name}
	for _, s := range texts {
		a.Parts = append(a.Parts, a2a.NewTextPart(s))
	}
	return a
}

func TestMessageText(t *testing.T) {
	if got := MessageText(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
	if got := MessageText(msg("  hello ", "world\n")); got != "hello world" {
		t.Errorf("got %q", got)
	}
	if got := MessageText(msg("  ", "\n")); got != "" {
		t.Errorf("whitespace = %q", got)
	}
}

func TestTaskText(t *testing.T) {
	if got := TaskText(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
	task := &a2a.Task{Artifacts: []*a2a.Artifact{
		art("answer", "foo", " bar"),
		art("halo.meta", "SECRET"),
		art("", "!\n"),
	}}
	if got := TaskText(task); got != "foo bar!" {
		t.Errorf("artifacts = %q", got)
	}

	status := a2a.TaskStatus{Message: msg(" from status ")}
	task = &a2a.Task{Artifacts: []*a2a.Artifact{art("a", " \n"), art("halo.x", "hidden")}, Status: status}
	if got := TaskText(task); got != "from status" {
		t.Errorf("fallback = %q", got)
	}
	if got := TaskText(&a2a.Task{Status: status}); got != "from status" {
		t.Errorf("no artifacts = %q", got)
	}
	if got := TaskText(&a2a.Task{}); got != "" {
		t.Errorf("empty task = %q", got)
	}
}

func TestText(t *testing.T) {
	if got := Text(msg(" m ")); got != "m" {
		t.Errorf("message = %q", got)
	}
	if got := Text(&a2a.Task{Artifacts: []*a2a.Artifact{art("x", "t")}}); got != "t" {
		t.Errorf("task = %q", got)
	}
	if got := Text(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
}
