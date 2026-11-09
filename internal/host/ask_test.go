package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// The MCP SDK infers ask_human's input schema from askHumanIn; the questions
// must come out as a typed array the lead can fill.
func TestAskHumanSchema(t *testing.T) {
	s, err := jsonschema.For[askHumanIn](nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	for _, want := range []string{`"questions"`, `"multi_select"`, `"options"`, `"label"`, `(Recommended)`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("schema lacks %s: %s", want, raw)
		}
	}
}
