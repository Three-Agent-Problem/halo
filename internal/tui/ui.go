package tui

import "strings"

// The panels, by the number key that focuses them.
const (
	pPlan    = 1
	pOrbit   = 2
	pAgent   = 3
	pComms   = 4
	pCommits = 5
	pEvents  = 6
	pRoster  = 7
	pShells  = 8
	nPanes   = 8
)

var paneName = [nPanes + 1]string{"", "plan", "orbit", "agent", "comms", "commits", "events", "roster", "shells"}

// Size is how much of the screen a panel takes. Growing takes space from the
// neighbours step by step: first the panel's column, then the orbit's width, then
// the whole screen. Shrinking below normal collapses a panel to a one-line bar.
type Size int

const (
	sizeMin    Size = iota - 1 // a one-line bar, like a closed accordion
	sizeNormal                 // the default layout
	sizeColumn                 // the panel takes its whole column (comms: half the height)
	sizeWide                   // its column also takes the orbit's width
	sizeFull                   // the panel takes the whole screen
)

func (z Size) String() string {
	switch z {
	case sizeMin:
		return "minimized"
	case sizeColumn:
		return "column"
	case sizeWide:
		return "wide"
	case sizeFull:
		return "full"
	}
	return "normal"
}

// ladder is the sizes panel id steps through. The orbit and comms are the
// frame of the layout, so they never collapse; the orbit has no column, and the
// comms grow upwards (column means taller) but never wider than the screen.
func ladder(id int) []Size {
	switch id {
	case pOrbit:
		return []Size{sizeNormal, sizeWide, sizeFull}
	case pComms:
		return []Size{sizeNormal, sizeColumn, sizeFull}
	}
	return []Size{sizeMin, sizeNormal, sizeColumn, sizeWide, sizeFull}
}

// columnOf is the panels that share a column with id in the wide layout, top to
// bottom, or nil for the orbit and comms. Commits and events share a slot.
func columnOf(id int) []int {
	switch id {
	case pPlan, pCommits, pEvents:
		return []int{pPlan, pCommits, pEvents}
	case pAgent, pShells, pRoster:
		return []int{pAgent, pShells, pRoster}
	}
	return nil
}

// cursor is a panel's selection and scroll state.
type pcursor struct {
	sel    int  // the selected item
	follow bool // keep the newest item selected
	off    int  // the first visible item
	detail bool // the selected item is open
	dOff   int  // the open item's scroll offset
	tail   bool // keep the open item scrolled to its end (live output)
}

// UI is the view state that isn't part of the run: focus, sizes, selections and
// overlays.
type UI struct {
	Focus  int
	Size   [nPanes + 1]Size
	Cur    [nPanes + 1]*pcursor
	Help   bool
	Query  string
	Filter string            // comms: only messages from or to this handle
	Diffs  map[string]string // commit SHA → git show output
	Calls  bool              // agent: the tool calls Lagrange saw, not the live transcript
	Out    map[int]string    // shell pid → the end of its output
	// BoardOff is the stacked plan board's scroll offset.
	BoardOff int
}

// NewUI focuses the comms panel.
func NewUI() *UI {
	u := &UI{Focus: pComms, Diffs: map[string]string{}, Out: map[int]string{}}
	for i := range u.Cur {
		u.Cur[i] = &pcursor{follow: i != pPlan && i != pRoster && i != pShells}
	}
	return u
}

func (u *UI) cur() *pcursor { return u.Cur[u.Focus] }

// Grow and Shrink step the focused panel along its ladder.
func (u *UI) Grow() { u.step(1) }

func (u *UI) Shrink() { u.step(-1) }

func (u *UI) step(d int) {
	l := ladder(u.Focus)
	i := 0
	for j, z := range l {
		if z == u.Size[u.Focus] {
			i = j
		}
	}
	u.Size[u.Focus] = l[max(0, min(len(l)-1, i+d))]
}

// grown is the focused panel's size when it is bigger than normal; only the
// focused panel can be, since moving the focus resets the others.
func (u *UI) grown() Size {
	if z := u.Size[u.Focus]; z > sizeNormal {
		return z
	}
	return sizeNormal
}

// SetFocus focuses panel id. Grown panels go back to normal so cycling through
// the panels shows the usual layout; minimized ones stay minimized.
func (u *UI) SetFocus(id int) {
	for i := range u.Size {
		if u.Size[i] > sizeNormal {
			u.Size[i] = sizeNormal
		}
	}
	u.Focus = id
}

// FocusNext moves the focus by d panels, wrapping around.
func (u *UI) FocusNext(d int) {
	u.SetFocus((u.Focus-1+d+nPanes)%nPanes + 1)
}

// Move moves the focused panel's selection (or the open item's scroll) by d,
// for a list of n items.
func (u *UI) Move(d, n int) {
	c := u.cur()
	if c.detail {
		c.dOff = max(0, c.dOff+d)
		c.tail = false
		return
	}
	if n == 0 {
		return
	}
	if c.follow {
		c.sel = n - 1
	}
	c.sel = max(0, min(n-1, c.sel+d))
	c.follow = c.sel == n-1 && d > 0 && u.Focus != pPlan && u.Focus != pRoster && u.Focus != pShells
}

// Top and Bottom jump to the first item, or to the newest and follow it.
func (u *UI) Top() {
	c := u.cur()
	if c.detail {
		c.dOff = 0
		return
	}
	c.sel, c.off, c.follow = 0, 0, false
}

func (u *UI) Bottom(n int) {
	c := u.cur()
	if c.detail {
		c.dOff, c.tail = 1<<20, true
		return
	}
	c.sel, c.follow = max(0, n-1), true
}

// listLines lays out items (each one or more lines) in h rows and keeps the
// selected one visible. Bottom-aligned lists (chat-like) fill from the bottom
// when they are short. The selected item is marked when the panel is focused.
func listLines(items [][]string, cur *pcursor, h int, focused, bottom bool) []string {
	n := len(items)
	if n == 0 || h <= 0 {
		return nil
	}
	if cur.follow {
		cur.sel = n - 1
	}
	cur.sel = max(0, min(n-1, cur.sel))
	height := func(i int) int { return max(1, len(items[i])) }
	if cur.follow {
		// Fill from the newest item upwards.
		cur.off = n - 1
		used := height(n - 1)
		for cur.off > 0 && used+height(cur.off-1) <= h {
			cur.off--
			used += height(cur.off)
		}
	} else {
		if cur.sel < cur.off {
			cur.off = cur.sel
		}
		for cur.off < cur.sel {
			used := 0
			for i := cur.off; i <= cur.sel; i++ {
				used += height(i)
			}
			if used <= h {
				break
			}
			cur.off++
		}
		cur.off = max(0, min(cur.off, n-1))
	}
	var out []string
	for i := cur.off; i < n && len(out) < h; i++ {
		for _, l := range items[i] {
			if len(out) == h {
				break
			}
			mark := "  "
			if focused && i == cur.sel {
				mark = c("hub!", "▌") + " "
			}
			out = append(out, mark+l)
		}
	}
	if bottom && len(out) < h {
		out = append(make([]string, h-len(out)), out...)
	}
	return out
}

// scrollLines shows lines from the open item's scroll offset.
func scrollLines(lines []string, c *pcursor, h int) []string {
	if h <= 0 {
		return nil
	}
	c.dOff = max(0, min(c.dOff, len(lines)-h))
	end := min(len(lines), c.dOff+h)
	return lines[c.dOff:end]
}

// matches reports whether every rune of q appears in s in order, ignoring case.
func matches(s, q string) bool {
	s, q = strings.ToLower(s), strings.ToLower(strings.TrimSpace(q))
	for _, r := range q {
		if r == ' ' {
			continue
		}
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}
