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
	nPanes   = 7
)

var paneName = [nPanes + 1]string{"", "plan", "orbit", "agent", "comms", "commits", "events", "roster"}

// Size is how much of the screen the focused panel takes, like lazygit's screen
// modes.
type Size int

const (
	sizeNormal Size = iota // the default layout
	sizeHalf               // the panel takes most of the screen, one companion stays
	sizeFull               // the panel takes the whole screen
)

func (z Size) String() string { return [...]string{"normal", "half", "full"}[z] }

// cursor is a panel's selection and scroll state.
type pcursor struct {
	sel    int  // the selected item
	follow bool // keep the newest item selected
	off    int  // the first visible item
	detail bool // the selected item is open
	dOff   int  // the open item's scroll offset
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
}

// NewUI focuses the comms panel; the events panel opens at half size since the
// default layout has no room for it.
func NewUI() *UI {
	u := &UI{Focus: pComms, Diffs: map[string]string{}}
	for i := range u.Cur {
		u.Cur[i] = &pcursor{follow: i != pPlan && i != pRoster}
	}
	u.Size[pEvents] = sizeHalf
	return u
}

func (u *UI) cur() *pcursor { return u.Cur[u.Focus] }

// Grow and Shrink step the focused panel's size.
func (u *UI) Grow() {
	if u.Size[u.Focus] < sizeFull {
		u.Size[u.Focus]++
	}
}

func (u *UI) Shrink() {
	if u.Size[u.Focus] > sizeNormal {
		u.Size[u.Focus]--
	}
}

// FocusNext moves the focus by d panels, wrapping around.
func (u *UI) FocusNext(d int) {
	u.Focus = (u.Focus-1+d+nPanes)%nPanes + 1
}

// Move moves the focused panel's selection (or the open item's scroll) by d,
// for a list of n items.
func (u *UI) Move(d, n int) {
	c := u.cur()
	if c.detail {
		c.dOff = max(0, c.dOff+d)
		return
	}
	if n == 0 {
		return
	}
	if c.follow {
		c.sel = n - 1
	}
	c.sel = max(0, min(n-1, c.sel+d))
	c.follow = c.sel == n-1 && d > 0 && u.Focus != pPlan && u.Focus != pRoster
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
		c.dOff = 1 << 20
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
