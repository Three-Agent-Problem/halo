package tui

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/three-agent-problem/halo/internal/plan"
)

// The plan studio's visuals. The lead sends their structure (plan.Block); these
// draw them on a grid of styled cells, so every block looks the same whoever
// planned it and costs the lead no tokens to lay out.

// drawn is a rendered block: its lines, and the first line of each selectable
// item (so the studio can keep the selection on screen).
type drawn struct {
	lines []string
	rows  []int
}

// sel is what the studio highlights in a block: the selected item (-1: none) and
// the subtask selected in the strip, whose parts are shown in bold.
type sel struct {
	item    int
	subtask string
}

// grid is a grid of runes, each with a palette key.
type grid struct {
	w, h int
	r    [][]rune
	k    [][]string
}

func newGrid(w, h int) *grid {
	cv := &grid{w: w, h: h, r: make([][]rune, h), k: make([][]string, h)}
	for y := range cv.r {
		cv.r[y] = []rune(strings.Repeat(" ", w))
		cv.k[y] = make([]string, w)
	}
	return cv
}

func (cv *grid) in(x, y int) bool { return x >= 0 && y >= 0 && x < cv.w && y < cv.h }

func (cv *grid) set(x, y int, r rune, key string) {
	if cv.in(x, y) {
		cv.r[y][x], cv.k[y][x] = r, key
	}
}

// text writes s from x, cut to n cells (n <= 0: to the edge).
func (cv *grid) text(x, y int, s, key string, n int) {
	rs := []rune(s)
	if n > 0 && len(rs) > n {
		rs = append(rs[:max(0, n-1)], '…')
	}
	for i, r := range rs {
		cv.set(x+i, y, r, key)
	}
}

// line draws a line glyph, joining it with a line already in the cell.
func (cv *grid) line(x, y int, r rune, key string) {
	if !cv.in(x, y) {
		return
	}
	old := cv.r[y][x]
	switch {
	case old == ' ' || old == r:
	case (old == '─' && r == '│') || (old == '│' && r == '─'):
		r = '┼'
	case strings.ContainsRune("▶┼", old):
		return
	case strings.ContainsRune("─│┌┐└┘├┤┬┴", old):
		r = '┼'
	}
	cv.set(x, y, r, key)
}

func (cv *grid) lines() []string {
	out := make([]string, cv.h)
	for y := range cv.r {
		var b strings.Builder
		start := 0
		for x := 1; x <= cv.w; x++ {
			if x == cv.w || cv.k[y][x] != cv.k[y][start] {
				b.WriteString(c(cv.k[y][start], string(cv.r[y][start:x])))
				start = x
			}
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// changeKey is the palette key of a part the run leaves, adds, changes or removes.
func changeKey(ch string) string {
	switch ch {
	case plan.New:
		return "haiku!"
	case plan.Changed:
		return "hub!"
	case plan.Removed:
		return "fail"
	}
	return "mute"
}

// changeMark is a file's or a step's marker.
func changeMark(ch string) string {
	switch ch {
	case plan.New:
		return "+"
	case plan.Changed:
		return "~"
	case plan.Removed:
		return "−"
	}
	return "·"
}

// borders are a node's box glyphs by its change: rounded for what exists,
// double for new, square for changed (in yellow), dashed for removed. No heavy
// lines: many terminal fonts lack them and draw them wider.
var borders = map[string][6]rune{
	"":           {'╭', '╮', '╰', '╯', '─', '│'},
	plan.New:     {'╔', '╗', '╚', '╝', '═', '║'},
	plan.Changed: {'┌', '┐', '└', '┘', '─', '│'},
	plan.Removed: {'┌', '┐', '└', '┘', '╌', '╎'},
}

func changeLegend() string {
	return c("mute", "╭─╮ exists  ") + c("haiku!", "╔═╗ new  ") + c("hub!", "┌─┐ changed  ") + c("fail", "┌╌┐ removed")
}

// drawBlock renders block b in w cells.
func drawBlock(b plan.Block, w int, s sel) drawn {
	switch b.Kind {
	case plan.KindArchitecture:
		return drawArch(b, w, s)
	case plan.KindFlow:
		return drawFlow(b, w, s)
	case plan.KindFiles:
		return drawFiles(b, w, s)
	case plan.KindCompare:
		return drawCompare(b, w, s)
	case plan.KindNote:
		return drawNote(b, w, s)
	}
	return drawn{lines: []string{c("dim", "a "+b.Kind+" block (this Halo can't draw it)")}}
}

// blockItems is how many selectable items block b has.
func blockItems(b plan.Block) int {
	switch b.Kind {
	case plan.KindArchitecture:
		return len(b.Nodes)
	case plan.KindFlow:
		return len(b.Steps)
	case plan.KindFiles:
		return len(b.Paths)
	case plan.KindCompare:
		return len(b.Rows)
	case plan.KindNote:
		return 1
	}
	return 0
}

// itemLabel names item i of block b in a note to the lead, and returns the
// subtask it belongs to, if any.
func itemLabel(b plan.Block, i int) (string, string) {
	title := nonEmpty(b.Title, b.Kind)
	switch b.Kind {
	case plan.KindArchitecture:
		if n := archOrder(b); i < len(n) {
			return title + " › " + n[i].Label, n[i].Subtask
		}
	case plan.KindFlow:
		if i < len(b.Steps) {
			return fmt.Sprintf("%s › step %d (%s)", title, i+1, b.Steps[i].Text), ""
		}
	case plan.KindFiles:
		if ps := sortedPaths(b); i < len(ps) {
			return ps[i].Path, ps[i].Subtask
		}
	case plan.KindCompare:
		if i < len(b.Rows) && len(b.Rows[i]) > 0 {
			return title + " › " + b.Rows[i][0], ""
		}
	case plan.KindNote:
		return "your note “" + firstLine(b.Text) + "”", ""
	}
	return title, ""
}

// ── architecture ─────────────────────────────────────────────────────────────

const (
	archGap  = 8 // cells between two columns, where arrows run
	archColW = 26
	archMinW = 12
)

// archColumns puts the nodes in columns: by group in the order the lead gave them
// (ungrouped nodes last), or without groups by how far each node is from a node
// nothing points at.
func archColumns(b plan.Block) (heads []string, cols [][]plan.Node) {
	if len(b.Groups) > 0 {
		at := map[string]int{}
		for i, g := range b.Groups {
			at[g.ID] = i
			heads = append(heads, g.Label)
			cols = append(cols, nil)
		}
		var rest []plan.Node
		for _, n := range b.Nodes {
			if i, ok := at[n.Group]; ok {
				cols[i] = append(cols[i], n)
			} else {
				rest = append(rest, n)
			}
		}
		if len(rest) > 0 {
			heads, cols = append(heads, ""), append(cols, rest)
		}
		// Drop empty groups.
		for i := len(cols) - 1; i >= 0; i-- {
			if len(cols[i]) == 0 {
				heads, cols = append(heads[:i], heads[i+1:]...), append(cols[:i], cols[i+1:]...)
			}
		}
		return heads, cols
	}
	layer := map[string]int{}
	for range b.Nodes { // longest path, at most n rounds (cycles stop there)
		moved := false
		for _, e := range b.Edges {
			if e.From != e.To && layer[e.To] < layer[e.From]+1 && layer[e.From]+1 < len(b.Nodes) {
				layer[e.To], moved = layer[e.From]+1, true
			}
		}
		if !moved {
			break
		}
	}
	for _, n := range b.Nodes {
		l := layer[n.ID]
		for len(cols) <= l {
			cols, heads = append(cols, nil), append(heads, "")
		}
		cols[l] = append(cols[l], n)
	}
	for i := len(cols) - 1; i >= 0; i-- {
		if len(cols[i]) == 0 {
			heads, cols = append(heads[:i], heads[i+1:]...), append(cols[:i], cols[i+1:]...)
		}
	}
	return heads, cols
}

// archOrder is the nodes in the order the studio selects them: column by column.
func archOrder(b plan.Block) []plan.Node {
	_, cols := archColumns(b)
	var out []plan.Node
	for _, col := range cols {
		out = append(out, col...)
	}
	return out
}

func drawArch(b plan.Block, w int, s sel) drawn {
	heads, cols := archColumns(b)
	n := len(cols)
	colW := min(archColW, (w-(n-1)*archGap)/max(1, n))
	if colW < archMinW {
		return drawArchList(b, w, s)
	}
	type spot struct{ col, row, idx int }
	at := map[string]spot{}
	idx, tall := 0, 0
	for ci, col := range cols {
		for ri, nd := range col {
			at[nd.ID] = spot{ci, ri, idx}
			idx++
		}
		tall = max(tall, len(col))
	}
	const top = 2 // group heads and their rule
	hasHeads := false
	for _, h := range heads {
		hasHeads = hasHeads || h != ""
	}
	y0 := 0
	if hasHeads {
		y0 = top
	}
	h := y0 + tall*4
	cv := newGrid(w, h)
	x := func(ci int) int { return ci * (colW + archGap) }
	mid := func(sp spot) int { return y0 + sp.row*4 + 1 }
	if hasHeads {
		for ci, hd := range heads {
			if hd == "" {
				continue
			}
			lw := min(len([]rune(hd)), colW)
			cv.text(x(ci)+(colW-lw)/2, 0, hd, "fg!", colW)
			for i := 0; i < colW; i++ {
				cv.set(x(ci)+i, 1, '╌', "line2")
			}
		}
	}
	// Edges first, so the boxes sit on top of them.
	// Only edges between neighbouring columns are drawn; the rest are listed below.
	tracks := map[int]int{}
	for _, e := range b.Edges {
		a, z := at[e.From], at[e.To]
		if z.col != a.col+1 {
			continue
		}
		key := changeKey(e.Change)
		if e.Change == "" || e.Change == plan.Existing {
			key = "dim"
		}
		gx := x(a.col) + colW // the first cell of the gap
		t := tracks[a.col]
		tracks[a.col]++
		tx := gx + 1 + t%(archGap-3)
		sy, ty := mid(a), mid(z)
		for i := gx; i < tx; i++ {
			cv.line(i, sy, '─', key)
		}
		switch {
		case ty > sy:
			cv.line(tx, sy, '┐', key)
			for y := sy + 1; y < ty; y++ {
				cv.line(tx, y, '│', key)
			}
			cv.line(tx, ty, '└', key)
		case ty < sy:
			cv.line(tx, sy, '┘', key)
			for y := ty + 1; y < sy; y++ {
				cv.line(tx, y, '│', key)
			}
			cv.line(tx, ty, '┌', key)
		default:
			cv.line(tx, sy, '─', key)
		}
		end := x(z.col) - 1
		for i := tx + 1; i < end; i++ {
			cv.line(i, ty, '─', key)
		}
		cv.set(end, ty, '▶', key)
	}
	rows := make([]int, idx)
	for ci, col := range cols {
		for ri, nd := range col {
			sp := at[nd.ID]
			rows[sp.idx] = y0 + ri*4
			ch := nd.Change
			if ch == plan.Existing {
				ch = ""
			}
			g, key := borders[ch], changeKey(ch)
			if ch == "" {
				key = "mute"
			}
			bx, by := x(ci), y0+ri*4
			cv.set(bx, by, g[0], key)
			for i := 1; i < colW-1; i++ {
				cv.set(bx+i, by, g[4], key)
				cv.set(bx+i, by+2, g[4], key)
			}
			cv.set(bx+colW-1, by, g[1], key)
			cv.set(bx, by+1, g[5], key)
			cv.set(bx+colW-1, by+1, g[5], key)
			cv.set(bx, by+2, g[2], key)
			cv.set(bx+colW-1, by+2, g[3], key)
			if nd.Subtask != "" {
				chip := " " + nd.Subtask + " "
				ck := key
				if nd.Subtask == s.subtask {
					ck = "cursor"
				}
				cv.text(bx+colW-1-len([]rune(chip))-1, by, chip, ck, 0)
			}
			label, lk := nd.Label, "fg"
			switch {
			case sp.idx == s.item:
				lk = "cursor"
			case nd.Subtask != "" && nd.Subtask == s.subtask:
				lk = "hi!"
			case ch == plan.Removed:
				lk = "struck"
			case ch == "":
				lk = "mute"
			}
			cv.text(bx+2, by+1, label, lk, colW-4)
			if sp.idx == s.item {
				cv.set(bx+1, by+1, '▸', "hub!")
			}
		}
	}
	lines := cv.lines()
	// Edges the grid can't draw, and the labels of the ones it did.
	var notes []string
	for _, e := range b.Edges {
		a, z := at[e.From], at[e.To]
		drawnHere := z.col == a.col+1
		if !drawnHere || e.Label != "" {
			key := changeKey(e.Change)
			arrow := " ─▶ "
			if !drawnHere {
				arrow = " ⤳ "
			}
			l := c(key, nodeLabel(b, e.From)+arrow+nodeLabel(b, e.To))
			if e.Label != "" {
				l += c("dim", "  "+e.Label)
			}
			notes = append(notes, "  "+l)
		}
	}
	if len(notes) > 0 {
		lines = append(lines, "")
		lines = append(lines, notes...)
	}
	lines = append(lines, "", "  "+changeLegend())
	return drawn{lines: lines, rows: rows}
}

func nodeLabel(b plan.Block, id string) string {
	for _, n := range b.Nodes {
		if n.ID == id {
			return n.Label
		}
	}
	return id
}

// drawArchList is the architecture as a list by group, for a narrow panel.
func drawArchList(b plan.Block, w int, s sel) drawn {
	heads, cols := archColumns(b)
	var lines []string
	var rows []int
	for ci, col := range cols {
		if heads[ci] != "" {
			lines = append(lines, c("fg!", heads[ci]))
		}
		for _, nd := range col {
			rows = append(rows, len(lines))
			mark, key := "●", changeKey(nd.Change)
			label := nd.Label
			lk := key
			switch {
			case len(rows)-1 == s.item:
				lk = "cursor"
			case nd.Subtask != "" && nd.Subtask == s.subtask:
				lk = "hi!"
			}
			l := "  " + c(key, mark) + " " + c(lk, ansi.Truncate(label, w-12, "…"))
			if nd.Subtask != "" {
				l += " " + c("dim", nd.Subtask)
			}
			lines = append(lines, l)
		}
	}
	for _, e := range b.Edges {
		l := "  " + c(changeKey(e.Change), nodeLabel(b, e.From)+" ─▶ "+nodeLabel(b, e.To))
		if e.Label != "" {
			l += c("dim", "  "+e.Label)
		}
		lines = append(lines, l)
	}
	lines = append(lines, "", c("haiku!", "● new ")+c("hub!", "● changed ")+c("fail", "● removed ")+c("mute", "● exists"))
	return drawn{lines: lines, rows: rows}
}

// ── flow ─────────────────────────────────────────────────────────────────────

func drawFlow(b plan.Block, w int, s sel) drawn {
	var actors []string
	seen := map[string]int{}
	for _, st := range b.Steps {
		for _, a := range []string{st.From, st.To} {
			if _, ok := seen[a]; a != "" && !ok {
				seen[a] = len(actors)
				actors = append(actors, a)
			}
		}
	}
	n := len(actors)
	colW := min(24, w/max(1, n))
	if colW < 8 {
		// Too narrow for lanes: a numbered list.
		var lines []string
		var rows []int
		for i, st := range b.Steps {
			rows = append(rows, len(lines))
			who := st.From
			if st.To != "" {
				who += " → " + st.To
			}
			key := pick(i == s.item, "cursor", changeKey(st.Change))
			lines = append(lines, c("dim", fmt.Sprintf("%2d ", i+1))+c(key, who+": "+st.Text))
		}
		return drawn{lines: lines, rows: rows}
	}
	cx := func(a string) int { return seen[a]*colW + colW/2 }
	h := 2 + len(b.Steps)*2 + 1
	cv := newGrid(w, h)
	for _, a := range actors {
		lw := min(len([]rune(a)), colW-2)
		cv.text(cx(a)-lw/2, 0, a, "fg!", colW-2)
		for y := 1; y < h; y++ {
			cv.set(cx(a), y, '│', "line2")
		}
	}
	rows := make([]int, len(b.Steps))
	for i, st := range b.Steps {
		y := 2 + i*2
		rows[i] = y
		key := changeKey(st.Change)
		if st.Change == "" || st.Change == plan.Existing {
			key = "mute"
		}
		from := cx(st.From)
		tk := pick(i == s.item, "cursor", pick(st.Change == plan.New || st.Change == plan.Changed, key, "fg"))
		label := fmt.Sprintf("%d %s", i+1, st.Text)
		if st.To == "" || st.To == st.From {
			cv.set(from, y, '●', key)
			cv.text(from+2, y, label, tk, w-from-2)
			continue
		}
		to := cx(st.To)
		lo, hi := min(from, to), max(from, to)
		cv.text(lo+2, y, label, tk, w-lo-2)
		cv.set(from, y+1, '●', key)
		for x := lo + 1; x < hi; x++ {
			cv.set(x, y+1, '─', key)
		}
		if to > from {
			cv.set(to-1, y+1, '▶', key)
		} else {
			cv.set(to+1, y+1, '◀', key)
		}
	}
	return drawn{lines: cv.lines(), rows: rows}
}

// ── files ────────────────────────────────────────────────────────────────────

func sortedPaths(b plan.Block) []plan.Path {
	ps := append([]plan.Path(nil), b.Paths...)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Path < ps[j].Path })
	return ps
}

func drawFiles(b plan.Block, w int, s sel) drawn {
	ps := sortedPaths(b)
	var lines []string
	rows := make([]int, len(ps))
	dir := "\x00"
	for i, p := range ps {
		d, f := path.Split(p.Path)
		if d != dir {
			dir = d
			lines = append(lines, c("mute", nonEmpty(d, "./")))
		}
		last := i == len(ps)-1 || path.Dir(ps[i+1].Path)+"/" != d
		branch := pick(last, "└─ ", "├─ ")
		key := changeKey(p.Change)
		fk := pick(i == s.item, "cursor", pick(p.Subtask != "" && p.Subtask == s.subtask, "hi!", key))
		l := c("line2", "  "+branch) + c(key, changeMark(p.Change)+" ") + c(fk, ansi.Truncate(f, max(8, w-20), "…"))
		if p.Subtask != "" {
			l += " " + c("dim", p.Subtask)
		}
		rows[i] = len(lines)
		lines = append(lines, l)
	}
	return drawn{lines: lines, rows: rows}
}

// ── compare ──────────────────────────────────────────────────────────────────

func drawCompare(b plan.Block, w int, s sel) drawn {
	n := len(b.Columns)
	want := make([]int, n)
	for i, col := range b.Columns {
		want[i] = ansi.StringWidth(col)
		for _, r := range b.Rows {
			if i < len(r) {
				want[i] = max(want[i], ansi.StringWidth(r[i]))
			}
		}
	}
	// Fit the columns into w: borders take n+1 cells, padding 2 per column.
	room := w - (n + 1) - 2*n
	cw := make([]int, n)
	total := 0
	for i := range want {
		cw[i] = max(3, want[i])
		total += cw[i]
	}
	for total > room {
		big := 0
		for i := range cw {
			if cw[i] > cw[big] {
				big = i
			}
		}
		if cw[big] <= 3 {
			break
		}
		cw[big]--
		total--
	}
	rule := func(l, m, r string) string {
		parts := make([]string, n)
		for i := range cw {
			parts[i] = strings.Repeat("─", cw[i]+2)
		}
		return c("line2", l+strings.Join(parts, m)+r)
	}
	row := func(cells []string, key string) string {
		var b strings.Builder
		b.WriteString(c("line2", "│"))
		for i := range cw {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			k := key
			if i == 0 && key == "fg" {
				k = "fg!"
			}
			b.WriteString(" " + c(k, fit(cell, cw[i])) + " " + c("line2", "│"))
		}
		return b.String()
	}
	lines := []string{rule("┌", "┬", "┐"), row(b.Columns, "mute!"), rule("├", "┼", "┤")}
	rows := make([]int, len(b.Rows))
	for i, r := range b.Rows {
		rows[i] = len(lines)
		lines = append(lines, row(r, pick(i == s.item, "cursor", "fg")))
	}
	lines = append(lines, rule("└", "┴", "┘"))
	return drawn{lines: lines, rows: rows}
}

// ── note ─────────────────────────────────────────────────────────────────────

func drawNote(b plan.Block, w int, s sel) drawn {
	icon, key := "ℹ", "task"
	switch b.Tone {
	case "warn":
		icon, key = "⚠", "ask"
	case "question":
		icon, key = "?", "hub"
	}
	var lines []string
	head := c(key+"!", icon+" "+nonEmpty(b.Title, b.Tone, "note"))
	if s.item == 0 {
		head = c("cursor", " "+icon+" "+nonEmpty(b.Title, b.Tone, "note")+" ")
	}
	lines = append(lines, c(key, "▌ ")+head)
	for _, l := range wrapLines(b.Text, w-4, 0) {
		lines = append(lines, c(key, "▌ ")+c("fg", l))
	}
	if b.Tone == "question" {
		lines = append(lines, c(key, "▌ ")+c("dim", "c answers it with your next notes"))
	}
	return drawn{lines: lines, rows: []int{0}}
}
