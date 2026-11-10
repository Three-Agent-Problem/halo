package host

import (
	"strings"
	"testing"
)

func TestLeadPromptListsOtherAgents(t *testing.T) {
	p := leadPrompt("halo-lead", "ada", map[string][]string{
		"sonnet":               {"@halo-sonnet-1"},
		"gemini-3.8-flash-low": {"@halo-agy-1"},
	}, "halo-reviewer")
	for _, want := range []string{
		"- sonnet: @halo-sonnet-1",
		"- gemini-3.8-flash-low: @halo-agy-1",
		"gemini-3.8-flash-low: not Claude",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("lead prompt is missing %q", want)
		}
	}
	if strings.Contains(p, "%!") {
		t.Errorf("lead prompt has a formatting error:\n%s", p)
	}
	if strings.Contains(leadPrompt("l", "ada", map[string][]string{"haiku": {"@w"}}, "r"), "not Claude") {
		t.Error("an all-Claude roster shouldn't get the other-agents line")
	}
}
