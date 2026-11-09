package tui

import (
	"sort"
	"strings"
)

// The commit graph: each commit is a row; branches are lanes two cells wide,
// like lazygit's. Lanes are laid out newest first (the way git walks history)
// and the rows are then flipped, since the panel lists the oldest commit first.

// maxLanes caps the graph's width; lanes past it are cut.
const maxLanes = 6

// gcell is one lane of a graph row: a glyph, its color, and whether a
// horizontal line runs on to the next lane.
type gcell struct {
	g    rune
	key  string
	line bool   // "─" in the gap after this lane
	lkey string // the gap's color
}

// grow is a commit's row and the lanes that run on below it (for the commit's
// extra detail lines).
type grow struct {
	cells []gcell
	cont  []gcell
}

// orderCommits sorts commits oldest first so that every commit comes after its
// parents (topological, newest-first by time among the ready ones). Times alone
// can tie: commits made in the same second.
func orderCommits(cs []Commit) []Commit {
	idx := map[string]int{}
	for i, c := range cs {
		idx[c.SHA] = i
	}
	children := make([]int, len(cs)) // children inside the set, not yet emitted
	for _, c := range cs {
		for _, p := range c.Parents {
			if j, ok := idx[p]; ok {
				children[j]++
			}
		}
	}
	done := make([]bool, len(cs))
	out := make([]Commit, 0, len(cs))
	for range cs {
		best := -1
		for i, c := range cs {
			if done[i] || children[i] > 0 {
				continue
			}
			if best < 0 || c.At.After(cs[best].At) || (c.At.Equal(cs[best].At) && i > best) {
				best = i
			}
		}
		if best < 0 { // a cycle can't happen in git; keep the rest by time
			var rest []Commit
			for i, c := range cs {
				if !done[i] {
					rest = append(rest, c)
				}
			}
			sort.SliceStable(rest, func(i, j int) bool { return rest[i].At.After(rest[j].At) })
			out = append(out, rest...)
			break
		}
		done[best] = true
		out = append(out, cs[best])
		for _, p := range cs[best].Parents {
			if j, ok := idx[p]; ok {
				children[j]--
			}
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// graph lays out commits (oldest first, as orderCommits sorts them) as rows of
// lanes. key is a commit's color.
func graph(cs []Commit, key func(Commit) string) []grow {
	in := map[string]bool{}
	for _, c := range cs {
		in[c.SHA] = true
	}
	type lane struct{ sha, key string }
	var lanes []lane
	free := func() int {
		for i, l := range lanes {
			if l.sha == "" {
				return i
			}
		}
		lanes = append(lanes, lane{})
		return len(lanes) - 1
	}
	rows := make([]grow, len(cs))
	for k := len(cs) - 1; k >= 0; k-- { // newest first
		c := cs[k]
		ck := key(c)
		var mine []int
		for i, l := range lanes {
			if l.sha == c.SHA {
				mine = append(mine, i)
			}
		}
		col := -1
		if len(mine) > 0 {
			col = mine[0]
		} else {
			col = free()
		}
		before := append([]lane(nil), lanes...)
		// The lanes that come down into this row are what runs below it once the
		// rows are flipped.
		var below []gcell
		for _, l := range before {
			if l.sha != "" {
				below = append(below, gcell{g: '│', key: l.key})
			} else {
				below = append(below, gcell{g: ' '})
			}
		}
		cells := make([]gcell, len(lanes))
		for i, l := range before {
			if l.sha != "" {
				cells[i] = gcell{g: '│', key: l.key}
			} else {
				cells[i] = gcell{g: ' '}
			}
		}
		glyph := '●'
		if len(c.Parents) > 1 {
			glyph = '◆'
		}
		cells[col] = gcell{g: glyph, key: ck}
		// Other lanes waiting for this commit end here, turning into its lane.
		for _, i := range mine[min(1, len(mine)):] {
			cells[i] = gcell{g: '╯', key: before[i].key}
			hline(cells, col, i, before[i].key)
			lanes[i] = lane{}
		}
		// The first parent (in the run's commits) continues this lane; the others
		// branch off into theirs.
		lanes[col] = lane{}
		cont := false
		for _, p := range c.Parents {
			if !in[p] {
				continue
			}
			if !cont {
				lanes[col], cont = lane{p, ck}, true
				continue
			}
			j := -1
			for i, l := range lanes {
				if l.sha == p {
					j = i
				}
			}
			if j < 0 {
				j = free()
				lanes[j] = lane{p, ck}
				for len(cells) < len(lanes) {
					cells = append(cells, gcell{g: ' '})
				}
			}
			g := '╮'
			if j < col {
				g = '╭'
			}
			if cells[j].g == '╯' || cells[j].g == '╰' {
				g = '┤'
			}
			cells[j] = gcell{g: g, key: lanes[j].key}
			hline(cells, col, j, lanes[j].key)
		}
		rows[k] = grow{cells: cells, cont: below}
	}
	// Flip vertically for the oldest-first list.
	flip := map[rune]rune{'╯': '╮', '╮': '╯', '╭': '╰', '╰': '╭'}
	for k := range rows {
		for i, cl := range rows[k].cells {
			if f, ok := flip[cl.g]; ok {
				rows[k].cells[i].g = f
			}
		}
	}
	return rows
}

// hline draws a horizontal line between lanes a and b (exclusive), crossing the
// vertical lines on the way.
func hline(cells []gcell, a, b int, key string) {
	lo, hi := min(a, b), max(a, b)
	for i := lo; i < hi; i++ {
		cells[i].line, cells[i].lkey = true, key
		if i > lo {
			switch cells[i].g {
			case '│':
				cells[i].g = '┼'
			case ' ':
				cells[i] = gcell{g: '─', key: key, line: true, lkey: key}
			}
		}
	}
}

// graphWidth is the cells the widest row needs, capped.
func graphWidth(rows []grow) int {
	n := 0
	for _, r := range rows {
		for i := len(r.cells) - 1; i >= 0; i-- {
			if r.cells[i].g != ' ' {
				n = max(n, i+1)
				break
			}
		}
	}
	return min(n, maxLanes) * 2
}

// renderCells draws lanes in w cells.
func renderCells(cells []gcell, w int) string {
	var b strings.Builder
	n := 0
	for i, cl := range cells {
		if n+2 > w {
			break
		}
		if i == maxLanes-1 && len(cells) > maxLanes {
			b.WriteString(c("dim", "… "))
			n += 2
			break
		}
		b.WriteString(c(cl.key+"!", string(cl.g)))
		if cl.line {
			b.WriteString(c(cl.lkey, "─"))
		} else {
			b.WriteString(" ")
		}
		n += 2
	}
	return b.String() + strings.Repeat(" ", max(0, w-n))
}
