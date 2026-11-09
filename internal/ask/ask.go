// Package ask is the multiple-choice questions the lead can put to the human,
// like Claude Code's plan-mode questions: each has a few options to pick one
// (or several) of, and the human can always write their own answer instead.
//
// The questions travel in the message metadata under MetaKey, next to a plain
// text rendering, so a client that doesn't know them (Lagrange's web inbox, an
// email answer link) still shows something to answer.
package ask

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MetaKey is the message metadata key that carries the questions.
const MetaKey = "https://threeagents.dev/ext/halo/questions"

// Limits keep a form small enough to answer at a glance.
const (
	MaxQuestions = 4
	MinOptions   = 2
	MaxOptions   = 5
)

// Option is one answer to pick.
type Option struct {
	Label       string `json:"label" jsonschema:"1-5 words; put the recommended option first and end its label with (Recommended)"`
	Description string `json:"description,omitempty" jsonschema:"one sentence: what picking it means, its trade-off"`
}

// Question is one question of a form.
type Question struct {
	Header      string   `json:"header" jsonschema:"a 1-2 word tag for the question, like Auth or Scope"`
	Question    string   `json:"question" jsonschema:"the whole question, ending with a question mark"`
	Options     []Option `json:"options" jsonschema:"2-5 distinct choices; the human can always write their own instead"`
	MultiSelect bool     `json:"multi_select,omitempty" jsonschema:"true when several options can apply together"`
}

// Validate checks a form against the limits.
func Validate(qs []Question) error {
	if len(qs) == 0 {
		return errors.New("no questions")
	}
	if len(qs) > MaxQuestions {
		return fmt.Errorf("at most %d questions, got %d", MaxQuestions, len(qs))
	}
	for i, q := range qs {
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("question %d has no text", i+1)
		}
		if len(q.Options) < MinOptions || len(q.Options) > MaxOptions {
			return fmt.Errorf("question %d needs %d-%d options, got %d", i+1, MinOptions, MaxOptions, len(q.Options))
		}
		seen := map[string]bool{}
		for _, o := range q.Options {
			l := strings.ToLower(strings.TrimSpace(o.Label))
			if l == "" || seen[l] {
				return fmt.Errorf("question %d has an empty or repeated option", i+1)
			}
			seen[l] = true
		}
	}
	return nil
}

// Text renders the questions as plain text, for clients without the form.
func Text(qs []Question) string {
	var b strings.Builder
	for i, q := range qs {
		if i > 0 {
			b.WriteString("\n")
		}
		head := q.Header
		if head == "" {
			head = fmt.Sprintf("Q%d", i+1)
		}
		fmt.Fprintf(&b, "%s: %s", head, q.Question)
		if q.MultiSelect {
			b.WriteString(" (pick any)")
		}
		b.WriteString("\n")
		for j, o := range q.Options {
			fmt.Fprintf(&b, "  %d. %s", j+1, o.Label)
			if o.Description != "" {
				b.WriteString(" — " + o.Description)
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Parse reads the questions from message metadata, or nil.
func Parse(meta map[string]any) []Question {
	v, ok := meta[MetaKey]
	if !ok {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var qs []Question
	if json.Unmarshal(raw, &qs) != nil || Validate(qs) != nil {
		return nil
	}
	return qs
}

// Answer is the human's answer to one question: the options picked (by
// index), and their own words, which may stand alone or add to the picks.
type Answer struct {
	Picked []int
	Other  string
}

// Format renders the answers for the lead, one line per question.
func Format(qs []Question, as []Answer) string {
	var lines []string
	for i, q := range qs {
		head := q.Header
		if head == "" {
			head = q.Question
		}
		var a Answer
		if i < len(as) {
			a = as[i]
		}
		var picks []string
		for _, p := range a.Picked {
			if p >= 0 && p < len(q.Options) {
				picks = append(picks, q.Options[p].Label)
			}
		}
		other := strings.TrimSpace(a.Other)
		switch {
		case len(picks) == 0 && other == "":
			lines = append(lines, head+": (no answer, your call)")
		case len(picks) == 0:
			lines = append(lines, head+": "+other)
		case other == "":
			lines = append(lines, head+": "+strings.Join(picks, ", "))
		default:
			lines = append(lines, head+": "+strings.Join(picks, ", ")+" (note: "+other+")")
		}
	}
	return strings.Join(lines, "\n")
}
