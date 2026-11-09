package tui

import (
	"context"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// maxCands caps the popup.
const maxCands = 8

// Candidate is one completion: Text replaces the token, Desc is shown beside it.
type Candidate struct {
	Text string
	Desc string
}

// Token is the word being typed: runes [Start,End) of the input, led by '@'
// (an agent), '#' (a subtask) or 'p' (a path).
type Token struct {
	Start, End int
	Kind       byte
	Text       string
}

// Sources is what completion draws on.
type Sources struct {
	Agents   []Candidate // Text is the short name (@sonnet-1), Desc the role
	Subtasks []Candidate // Text is #S3, Desc the title
	Files    []string
}

// FindToken returns the word under the cursor (a rune offset) when it is one
// that completes: an @mention, a #subtask or a path.
func FindToken(value string, cursor int) (Token, bool) {
	r := []rune(value)
	cursor = max(0, min(cursor, len(r)))
	start := cursor
	for start > 0 && !unicode.IsSpace(r[start-1]) {
		start--
	}
	end := cursor
	for end < len(r) && !unicode.IsSpace(r[end]) {
		end++
	}
	if start == end {
		return Token{}, false
	}
	t := Token{Start: start, End: end, Text: string(r[start:end])}
	switch {
	case t.Text[0] == '@':
		t.Kind = '@'
	case t.Text[0] == '#':
		t.Kind = '#'
	case strings.ContainsAny(t.Text, "/."):
		t.Kind = 'p'
	default:
		return Token{}, false
	}
	return t, true
}

// Complete finds the token at the cursor and its candidates.
func Complete(value string, cursor int, src Sources) (Token, []Candidate) {
	tok, ok := FindToken(value, cursor)
	if !ok {
		return tok, nil
	}
	// Only what is left of the cursor is matched, so editing mid-word works.
	r := []rune(value)
	q := string(r[tok.Start:min(cursor, tok.End)])
	var out []Candidate
	switch tok.Kind {
	case '@':
		out = rankCands(src.Agents, q, false)
	case '#':
		out = rankCands(src.Subtasks, q, true)
	default:
		out = rankPaths(src.Files, strings.TrimPrefix(q, "./"))
	}
	// A candidate equal to what is typed has nothing left to complete.
	if len(out) == 1 && out[0].Text == tok.Text && tok.Kind != 'p' {
		return tok, nil
	}
	return tok, out
}

// rankCands orders by prefix match on the name, then on the description.
func rankCands(all []Candidate, q string, desc bool) []Candidate {
	q = strings.ToLower(q)
	type scored struct {
		c Candidate
		s int
	}
	var m []scored
	for _, c := range all {
		name := strings.ToLower(c.Text)
		switch {
		case strings.HasPrefix(name, q):
			m = append(m, scored{c, 0})
		case strings.Contains(name, q[min(1, len(q)):]) && len(q) > 1:
			m = append(m, scored{c, 1})
		case desc && len(q) > 1 && strings.Contains(strings.ToLower(c.Desc), q[1:]):
			m = append(m, scored{c, 2})
		}
	}
	sort.SliceStable(m, func(i, j int) bool { return m[i].s < m[j].s })
	var out []Candidate
	for _, s := range m {
		out = append(out, s.c)
	}
	return out[:min(len(out), maxCands)]
}

// rankPaths orders prefix matches of the path, then of the file name, then
// substrings, then subsequences; shorter paths first within a class.
func rankPaths(files []string, q string) []Candidate {
	if q == "" {
		return nil
	}
	lq := strings.ToLower(q)
	type scored struct {
		p string
		s int
	}
	var m []scored
	for _, f := range files {
		lf := strings.ToLower(f)
		base := lf[strings.LastIndex(lf, "/")+1:]
		switch {
		case strings.HasPrefix(lf, lq):
			m = append(m, scored{f, 0})
		case strings.HasPrefix(base, lq):
			m = append(m, scored{f, 1})
		case strings.Contains(lf, lq):
			m = append(m, scored{f, 2})
		case subsequence(lf, lq):
			m = append(m, scored{f, 3})
		}
	}
	sort.SliceStable(m, func(i, j int) bool {
		if m[i].s != m[j].s {
			return m[i].s < m[j].s
		}
		if len(m[i].p) != len(m[j].p) {
			return len(m[i].p) < len(m[j].p)
		}
		return m[i].p < m[j].p
	})
	var out []Candidate
	for _, s := range m {
		out = append(out, Candidate{Text: s.p})
	}
	return out[:min(len(out), maxCands)]
}

func subsequence(s, q string) bool {
	i := 0
	for _, r := range s {
		if i < len(q) && rune(q[i]) == r {
			i++
		}
	}
	return i == len(q)
}

// Accept replaces tok with c and a trailing space; it returns the new value and
// the cursor (a rune offset) after the space.
func Accept(value string, tok Token, c Candidate) (string, int) {
	r := []rune(value)
	ins := []rune(c.Text + " ")
	rest := r[tok.End:]
	if len(rest) > 0 && unicode.IsSpace(rest[0]) {
		rest = rest[1:] // the space that follows is reused
	}
	out := append(append(append([]rune{}, r[:tok.Start]...), ins...), rest...)
	return string(out), tok.Start + len(ins)
}

var mentionRe = regexp.MustCompile(`(^|[^\w@])@([A-Za-z0-9_-]+)`)

// ExpandMentions turns short @mentions into full handles (@sonnet-1 into
// @halo-sonnet-1). short maps the name without '@' to the handle.
func ExpandMentions(text string, short map[string]string) string {
	return mentionRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := mentionRe.FindStringSubmatch(m)
		if h, ok := short[sub[2]]; ok {
			return sub[1] + "@" + h
		}
		return m
	})
}

// sources gathers the completion candidates from the state.
func (m *model) sources() Sources {
	s := Sources{Files: m.files}
	for _, h := range m.st.Order {
		role := ""
		if a := m.st.Agents[h]; a != nil {
			role = a.Role
		}
		s.Agents = append(s.Agents, Candidate{Text: m.st.Short(h), Desc: role})
	}
	if m.st.Plan != nil {
		for _, st := range m.st.Plan.Subtasks {
			s.Subtasks = append(s.Subtasks, Candidate{Text: "#" + st.ID, Desc: st.Title})
		}
	}
	return s
}

// shortMap maps short names to handles for ExpandMentions.
func (m *model) shortMap() map[string]string {
	out := map[string]string{}
	for h := range m.st.Agents {
		out[strings.TrimPrefix(m.st.Short(h), "@")] = h
	}
	for _, h := range m.st.Order {
		out[strings.TrimPrefix(m.st.Short(h), "@")] = h
	}
	return out
}

// GitFiles lists the files git tracks in repo.
func GitFiles(ctx context.Context, repo string) ([]string, error) {
	if repo == "" {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "git", "ls-files")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n"), nil
}
