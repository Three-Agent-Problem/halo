package tui

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// cell is one character of the orbit canvas with a palette key ("" is blank).
type cell struct {
	r     rune
	style string
}

type canvas struct {
	w, h  int
	cells [][]cell
	solid [][]bool // occupied by a box: lines and packets don't draw here
}

func newCanvas(w, h int) *canvas {
	cv := &canvas{w: w, h: h, cells: make([][]cell, h), solid: make([][]bool, h)}
	for y := range cv.cells {
		cv.cells[y] = make([]cell, w)
		cv.solid[y] = make([]bool, w)
		for x := range cv.cells[y] {
			cv.cells[y][x] = cell{' ', ""}
		}
	}
	return cv
}

func (cv *canvas) set(x, y int, r rune, style string) {
	if x < 0 || y < 0 || x >= cv.w || y >= cv.h || cv.solid[y][x] {
		return
	}
	cv.cells[y][x] = cell{r, style}
}

func (cv *canvas) text(x, y int, s, style string) {
	for _, r := range s {
		cv.set(x, y, r, style)
		x++
	}
}

func (cv *canvas) fillSolid(x, y, w, h int) {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			if i >= 0 && j >= 0 && i < cv.w && j < cv.h {
				cv.cells[j][i] = cell{' ', ""}
				cv.solid[j][i] = true
			}
		}
	}
}

// put writes onto solid cells too (the boxes themselves).
func (cv *canvas) put(x, y int, s, style string) {
	for _, r := range s {
		if x >= 0 && y >= 0 && x < cv.w && y < cv.h {
			cv.cells[y][x] = cell{r, style}
		}
		x++
	}
}

func (cv *canvas) String() string {
	var b strings.Builder
	for y, row := range cv.cells {
		if y > 0 {
			b.WriteByte('\n')
		}
		var run strings.Builder
		cur := "\x00"
		flush := func() {
			if run.Len() > 0 {
				b.WriteString(c(cur, run.String()))
				run.Reset()
			}
		}
		for _, cl := range row {
			if cl.style != cur {
				flush()
				cur = cl.style
			}
			run.WriteRune(cl.r)
		}
		flush()
	}
	return b.String()
}

type pt struct{ x, y int }

// line returns the cells from a to b (Bresenham).
func line(a, b pt) []pt {
	var out []pt
	dx, dy := abs(b.x-a.x), -abs(b.y-a.y)
	sx, sy := 1, 1
	if a.x > b.x {
		sx = -1
	}
	if a.y > b.y {
		sy = -1
	}
	err := dx + dy
	for {
		out = append(out, a)
		if a == b {
			return out
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			a.x += sx
		}
		if e2 <= dx {
			err += dx
			a.y += sy
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

const (
	nodeW = 24
	nodeH = 4
	hubW  = 33
	hubH  = 5
)

// orbit draws the agents around the Lagrange hub with the traffic between them.
func (s *State) orbit(w, h int, now time.Time, frame int) string {
	cv := newCanvas(w, h)
	if w < nodeW*2+hubW || h < nodeH*2+hubH {
		cv.text(1, 1, "window too small for the orbit", "dim")
		return cv.String()
	}
	hub := pt{w / 2, h / 2}
	rx := float64(w/2 - nodeW/2 - 1)
	ry := float64(h/2 - nodeH/2)
	pos := map[string]pt{}
	n := len(s.Order)
	for i, hd := range s.Order {
		a := -math.Pi/2 + float64(i)*2*math.Pi/float64(n)
		pos[hd] = pt{hub.x + int(math.Round(math.Cos(a)*rx)), hub.y + int(math.Round(math.Sin(a)*ry))}
	}

	// Orbit rings, slowly turning.
	for ring, f := range []float64{.45, .8} {
		steps := int(2 * math.Pi * rx * f / 2)
		for i := 0; i < steps; i++ {
			dir := 1
			if ring == 1 {
				dir = -1
			}
			if (i+dir*frame/4)%5 != 0 {
				continue
			}
			a := float64(i) / float64(steps) * 2 * math.Pi
			cv.set(hub.x+int(math.Cos(a)*rx*f), hub.y+int(math.Sin(a)*ry*f), '·', "line2")
		}
	}

	// Boxes are solid: reserve them before drawing lines.
	for _, hd := range s.Order {
		p := pos[hd]
		cv.fillSolid(p.x-nodeW/2, p.y-nodeH/2, nodeW, nodeH)
	}
	cv.fillSolid(hub.x-hubW/2, hub.y-hubH/2, hubW, hubH)

	// Edges: dotted, marching when the agent is busy.
	paths := map[string][]pt{}
	for _, hd := range s.Order {
		a := s.Agents[hd]
		path := line(pos[hd], hub)
		paths[hd] = path
		hot := a.State == "working" || a.State == "asking" || a.State == "waiting" || now.Sub(a.LastAct) < 1500*time.Millisecond
		for i, q := range path {
			switch {
			case hot && (i-frame/2)%3 == 0:
				cv.set(q.x, q.y, '•', modelKey(a.Model))
			case hot:
				cv.set(q.x, q.y, '·', modelKey(a.Model))
			case i%2 == 0:
				cv.set(q.x, q.y, '·', "line2")
			}
		}
	}

	// Streamed chunks: small dots from an agent to the hub.
	for _, d := range s.Dots {
		path := paths[d.From]
		if len(path) == 0 {
			continue
		}
		f := float64(now.Sub(d.Start)) / float64(time.Second)
		i := int(f * float64(len(path)-1))
		if i >= 0 && i < len(path) {
			cv.set(path[i].x, path[i].y, '∙', modelKey(s.model(d.From)))
		}
	}

	// Packets: sender → hub → recipient, a bright head and a fading trail.
	for _, p := range s.Packets {
		f := float64(now.Sub(p.Start)) / float64(PacketTime)
		if f < 0 || f > 1 {
			continue
		}
		in, out := paths[p.From], paths[p.To]
		if len(in) == 0 || len(out) == 0 {
			continue
		}
		route := append(append([]pt{}, in...), reverse(out)...)
		head := int(f * float64(len(route)-1))
		key := kindKey(p.Kind)
		for k := 4; k >= 0; k-- {
			i := head - k
			if i < 0 {
				continue
			}
			r := '▪'
			if k == 0 {
				r = '■'
			}
			cv.set(route[i].x, route[i].y, r, key)
		}
		if p.Label != "" {
			q := route[head]
			cv.text(q.x+2, q.y, p.Label, key+"!")
		}
	}

	// The hub.
	flash := now.Sub(s.HubAt) < 400*time.Millisecond
	hx, hy := hub.x-hubW/2, hub.y-hubH/2
	hk := "hub"
	if flash {
		hk = "hub!"
	}
	rate, _ := s.Rate(now, 1)
	cv.put(hx, hy, "╔"+strings.Repeat("═", hubW-2)+"╗", hk)
	for j := 1; j < hubH-1; j++ {
		cv.put(hx, hy+j, "║", hk)
		cv.put(hx+hubW-1, hy+j, "║", hk)
	}
	cv.put(hx, hy+hubH-1, "╚"+strings.Repeat("═", hubW-2)+"╝", hk)
	center := func(y int, s, style string) {
		cv.put(hub.x-len([]rune(s))/2, y, s, style)
	}
	center(hy+1, "L A G R A N G E", "hub!")
	center(hy+2, "relay · mailbox · inbox · runs", "mute")
	stat := fmt.Sprintf("⇅ %.1f ev/s · %d in flight", rate, len(s.Packets))
	if flash {
		center(hy+3, stat, "hi")
	} else {
		center(hy+3, stat, "dim")
	}

	// The agents.
	for _, hd := range s.Order {
		s.node(cv, s.Agents[hd], pos[hd], now, frame)
	}
	return cv.String()
}

func reverse(p []pt) []pt {
	out := make([]pt, len(p))
	for i := range p {
		out[i] = p[len(p)-1-i]
	}
	return out
}

func kindKey(kind string) string {
	switch kind {
	case "task", "ask", "reply", "review", "fail", "hub":
		return kind
	}
	return "fg"
}

func (s *State) node(cv *canvas, a *Agent, p pt, now time.Time, frame int) {
	x, y := p.x-nodeW/2, p.y-nodeH/2
	mk := modelKey(a.Model)
	hot := a.State == "working" || a.State == "asking" || a.State == "waiting" || now.Sub(a.LastAct) < 1200*time.Millisecond
	border := "line2"
	if hot {
		border = mk
	}
	title := s.Short(a.Handle)
	if a.Role == "human" {
		title += " (you)"
	}
	label := strings.ToUpper(a.Model)
	if a.Model == "fg" {
		label = "AGENT"
	}
	// Top border: ┌ @name ──── MODEL ┐
	cv.put(x, y, "┌ ", border)
	tk := "fg"
	if hot {
		tk = "hi!"
	}
	cv.put(x+2, y, title, tk)
	used := 2 + len([]rune(title)) + 1
	fill := nodeW - used - len(label) - 3
	cv.put(x+used-1, y, " "+strings.Repeat("─", max(fill, 0))+" ", border)
	lk := mk
	if !hot {
		lk = "dim"
	}
	cv.put(x+nodeW-len(label)-2, y, label, lk+"!")
	cv.put(x+nodeW-2, y, " ┐", border)
	cv.put(x, y+1, "│", border)
	cv.put(x+nodeW-1, y+1, "│", border)
	cv.put(x, y+2, "│", border)
	cv.put(x+nodeW-1, y+2, "│", border)
	cv.put(x, y+3, "└"+strings.Repeat("─", nodeW-2)+"┘", border)

	// Line 1: state.
	icon, sk := "·", "dim"
	switch a.State {
	case "working":
		icon, sk = string(spin[frame%len(spin)]), mk
	case "asking":
		icon, sk = "?", "ask"
	case "waiting":
		icon, sk = "◌", "ask"
	case "failed":
		icon, sk = "✗", "fail"
	}
	if !a.Online && a.Role != "human" {
		icon, sk = "○", "dim"
	}
	st := a.State
	if !a.Online && a.Role != "human" {
		st = "offline"
	}
	l1 := icon + " " + st
	if a.Sub != "" && a.State != "idle" {
		l1 += "  " + a.Sub
	}
	cv.put(x+2, y+1, fitPlain(l1, nodeW-4), sk)

	// Line 2: progress for a worker, else the last thing it did.
	inner := nodeW - 4
	if a.State == "working" && a.Sub != "" && s.Plan != nil && a.Role == "worker" {
		if st := s.Plan.Get(a.Sub); st != nil {
			el := mmss(now.Sub(a.Since))
			cv.put(x+2, y+2, bar(s.SubProgress(st, now), inner-len(el)-1), mk)
			cv.put(x+2+inner-len(el), y+2, el, "dim")
			return
		}
	}
	last := ""
	if len(a.Log) > 0 {
		last = strings.Trim(strings.TrimPrefix(a.Log[len(a.Log)-1], "▸ "), "“”")
	} else if a.Role == "human" {
		last = "human · inbox"
	} else {
		last = "ready"
	}
	cv.put(x+2, y+2, fitPlain(last, inner), "mute")
}

func fitPlain(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s + strings.Repeat(" ", n-len(r))
}

func mmss(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
