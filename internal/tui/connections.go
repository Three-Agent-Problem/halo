package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/three-agent-problem/lagrange/client"
	"rsc.io/qr"
)

// Connections: /connections links your own Telegram bot to Lagrange, so what waits on
// you reaches your phone, with buttons to answer, and runs report there. The bot lives
// in Lagrange (Settings → Telegram is the same thing); Halo walks you through it.

// Connector is a Backend that can connect Telegram.
type Connector interface {
	Telegram(ctx context.Context) (*client.Telegram, error)
	ConnectTelegram(ctx context.Context, token string) (*client.Telegram, error)
	PairTelegram(ctx context.Context) (*client.Telegram, error)
	SetTelegramLevel(ctx context.Context, level string) (*client.Telegram, error)
	TestTelegram(ctx context.Context) error
	DisconnectTelegram(ctx context.Context) error
}

// Telegram implements Connector.
func (l *Live) Telegram(ctx context.Context) (*client.Telegram, error) {
	return l.client().Telegram(ctx)
}

// ConnectTelegram implements Connector.
func (l *Live) ConnectTelegram(ctx context.Context, token string) (*client.Telegram, error) {
	return l.client().ConnectTelegram(ctx, token)
}

// PairTelegram implements Connector.
func (l *Live) PairTelegram(ctx context.Context) (*client.Telegram, error) {
	return l.client().PairTelegram(ctx)
}

// SetTelegramLevel implements Connector.
func (l *Live) SetTelegramLevel(ctx context.Context, level string) (*client.Telegram, error) {
	return l.client().SetTelegramLevel(ctx, level)
}

// DisconnectTelegram implements Connector.
func (l *Live) DisconnectTelegram(ctx context.Context) error {
	return l.client().DisconnectTelegram(ctx)
}

// TestTelegram implements Connector.
func (l *Live) TestTelegram(ctx context.Context) error { return l.client().TestTelegram(ctx) }

// The demo's bot pairs a few seconds after its link is made.
var demoTG = struct {
	st     client.Telegram
	linkAt time.Time
}{}

// Telegram implements Connector.
func (d *Demo) Telegram(context.Context) (*client.Telegram, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := demoTG.st
	if st.Connected && !st.Paired && !demoTG.linkAt.IsZero() && time.Since(demoTG.linkAt) > 4*time.Second {
		demoTG.st.Paired, demoTG.st.ChatUsername, demoTG.st.PairLink = true, d.Human, ""
		st = demoTG.st
	}
	return &st, nil
}

// ConnectTelegram implements Connector.
func (d *Demo) ConnectTelegram(ctx context.Context, token string) (*client.Telegram, error) {
	if !strings.Contains(token, ":") || len(token) < 20 {
		return nil, &client.Error{Status: 400, Message: "that isn't a working bot token: copy the whole token @BotFather sent (digits, a colon, then letters)"}
	}
	d.mu.Lock()
	demoTG.st = client.Telegram{Connected: true, BotUsername: d.Human + "_halo_bot", Level: "milestones"}
	d.mu.Unlock()
	return d.PairTelegram(ctx)
}

// PairTelegram implements Connector.
func (d *Demo) PairTelegram(context.Context) (*client.Telegram, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	demoTG.linkAt = time.Now()
	demoTG.st.PairLink = "https://t.me/" + demoTG.st.BotUsername + "?start=demo4x7k2q9mz"
	st := demoTG.st
	return &st, nil
}

// SetTelegramLevel implements Connector.
func (d *Demo) SetTelegramLevel(_ context.Context, level string) (*client.Telegram, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	demoTG.st.Level = level
	st := demoTG.st
	return &st, nil
}

// TestTelegram implements Connector.
func (d *Demo) TestTelegram(context.Context) error { return nil }

// DisconnectTelegram implements Connector.
func (d *Demo) DisconnectTelegram(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	demoTG.st, demoTG.linkAt = client.Telegram{}, time.Time{}
	return nil
}

// connWizard is the Telegram dialog: /connections in the TUI, and a section of
// halo setup. It talks to Lagrange through c.
type connWizard struct {
	c     Connector
	ctx   context.Context
	st    *client.Telegram
	token string
	link  string
	busy  bool
	err   string
	note  string
	armed bool // d was pressed once: d again disconnects
}

type (
	tgMsg struct {
		st   *client.Telegram
		err  error
		note string
		poll bool // a background check, not an action
	}
	tgPollMsg struct{}
)

func newConnWizard(ctx context.Context, c Connector) (*connWizard, tea.Cmd) {
	w := &connWizard{c: c, ctx: ctx, busy: true}
	return w, w.do(func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.Telegram(ctx) }, "", false)
}

func (m *model) connector() Connector {
	c, _ := m.o.Backend.(Connector)
	return c
}

// openConnections opens the dialog and reads the connection.
func (m *model) openConnections() tea.Cmd {
	if m.connector() == nil {
		m.say("connections need a Lagrange server")
		return nil
	}
	var cmd tea.Cmd
	m.conn, cmd = newConnWizard(m.ctx, m.connector())
	return cmd
}

// tgDo runs a Connector call in the background, outside any dialog.
func tgDo(ctx context.Context, c Connector, f func(context.Context, Connector) (*client.Telegram, error), note string, poll bool) tea.Cmd {
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		st, err := f(ctx, c)
		return tgMsg{st: st, err: err, note: note, poll: poll}
	}
}

func (w *connWizard) do(f func(context.Context, Connector) (*client.Telegram, error), note string, poll bool) tea.Cmd {
	return tgDo(w.ctx, w.c, f, note, poll)
}

func tgPoll() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tgPollMsg{} }) }

// tgUpdate handles background Telegram results in the TUI.
func (m *model) tgUpdate(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tgMsg); ok && msg.st != nil && msg.err == nil {
		m.tgPaired = msg.st.Paired
	}
	if m.conn == nil {
		return nil
	}
	return m.conn.update(msg)
}

// update handles the dialog's background results.
func (w *connWizard) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tgMsg:
		w.busy = false
		if msg.err != nil {
			if !msg.poll {
				w.err = msg.err.Error()
			}
			return nil
		}
		wasPaired := w.st != nil && w.st.Paired
		w.st, w.err = msg.st, ""
		if msg.st.PairLink != "" {
			w.link = msg.st.PairLink
		}
		if msg.st.Paired && !wasPaired && msg.poll {
			w.link, w.note = "", "✓ paired. Questions for you will arrive in Telegram too."
		} else if msg.note != "" {
			w.note = msg.note
		}
		if msg.st.Connected && !msg.st.Paired && w.link != "" {
			return tgPoll()
		}
	case tgPollMsg:
		if w.st == nil || w.st.Paired {
			return nil
		}
		return w.do(func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.Telegram(ctx) }, "", true)
	}
	return nil
}

// connKey handles a key while the dialog is open.
func (m *model) connKey(k tea.KeyPressMsg) tea.Cmd {
	if k.String() == "esc" {
		m.conn = nil
		return nil
	}
	return m.conn.key(k)
}

// key handles a key (esc is the caller's).
func (w *connWizard) key(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if w.busy {
		return nil
	}
	w.err, w.note = "", ""
	armed := w.armed
	w.armed = false
	switch {
	case w.st == nil:
		return nil
	case w.st.Connected && key == "d":
		if !armed {
			w.armed, w.note = true, "press d again to disconnect @"+w.st.BotUsername+" (it forgets the token and your chat)"
			return nil
		}
		w.busy, w.link = true, ""
		return w.do(func(ctx context.Context, c Connector) (*client.Telegram, error) {
			if err := c.DisconnectTelegram(ctx); err != nil {
				return nil, err
			}
			return c.Telegram(ctx)
		}, "disconnected: paste a token to connect a bot again", false)
	case !w.st.Connected:
		switch key {
		case "enter":
			token := strings.TrimSpace(w.token)
			if token == "" {
				return nil
			}
			w.busy, w.token = true, ""
			return w.do(func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.ConnectTelegram(ctx, token) }, "", false)
		case "backspace":
			if r := []rune(w.token); len(r) > 0 {
				w.token = string(r[:len(r)-1])
			}
		case "ctrl+u":
			w.token = ""
		default:
			if t := k.Text; t != "" {
				w.token += t
			}
		}
	default:
		switch key {
		case "p":
			w.busy = true
			return w.do(func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.PairTelegram(ctx) }, "", false)
		case "l":
			if !w.st.Paired {
				return nil
			}
			next := "questions"
			if w.st.Level == "questions" {
				next = "milestones"
			}
			w.busy = true
			return w.do(func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.SetTelegramLevel(ctx, next) }, "saved", false)
		case "t":
			if !w.st.Paired {
				return nil
			}
			w.busy = true
			return w.do(func(ctx context.Context, c Connector) (*client.Telegram, error) {
				if err := c.TestTelegram(ctx); err != nil {
					return nil, err
				}
				return c.Telegram(ctx)
			}, "test sent: check Telegram", false)
		}
	}
	return nil
}

// connPaste takes a pasted token.
func (m *model) connPaste(s string) {
	if m.conn != nil {
		m.conn.paste(s)
	}
}

func (w *connWizard) paste(s string) {
	if w.st != nil && !w.st.Connected {
		w.token += strings.TrimSpace(s)
	}
}

// connBox draws the dialog.
func (m *model) connBox(width int) []string {
	lines := m.conn.lines(width-6, m.frame, "close")
	return strings.Split(box(width, len(lines)+2, c("task!", "TELEGRAM"), "", lines, true), "\n")
}

// lines draws the dialog's content, inner cells wide; esc does what escWord says.
func (w *connWizard) lines(inner, frame int, escWord string) []string {
	say := func(style, s string) []string {
		var out []string
		for _, l := range strings.Split(ansi.Wordwrap(s, inner, ""), "\n") {
			out = append(out, "  "+c(style, l))
		}
		return out
	}
	key := func(k, what string) string { return c("reply!", k) + c("mute", " "+what+"   ") }
	esc := key("esc", escWord)
	lines := []string{""}
	lines = append(lines, say("fg", "Answer Halo's questions and follow its runs from your phone, through your own Telegram bot on Lagrange.")...)
	lines = append(lines, say("mute", "The bot only answers with buttons: approve, reject, pick an option. It can't start, stop or message anything, and typed text goes nowhere.")...)
	lines = append(lines, "")
	switch st := w.st; {
	case st == nil:
		lines = append(lines, "  "+c("mute", "reading the connection…"))
	case !st.Connected:
		lines = append(lines,
			"  "+c("hub!", "1")+c("fg", "  In Telegram, open ")+c("hi!", "@BotFather")+c("fg", " and send ")+c("hi!", "/newbot"),
			"  "+c("hub!", "2")+c("fg", "  Pick a name, then a username that ends in “bot”"),
			"  "+c("hub!", "3")+c("fg", "  Paste the token it sends you here"),
			"")
		masked := strings.Repeat("•", min(len([]rune(w.token)), max(0, inner-12)))
		cursor := c("reply!", "▏")
		if w.busy {
			cursor = c("mute", " checking with Telegram…")
		}
		lines = append(lines, "  "+c("mute", "token  ")+c("hi", masked)+cursor, "", "  "+key("enter", "connect")+esc)
	case !st.Paired:
		lines = append(lines, say("fg", "Connected to @"+st.BotUsername+". Now open it and tap Start: that pairs it with your chat.")...)
		if w.link != "" {
			lines = append(lines, "")
			for _, l := range QR(w.link) {
				lines = append(lines, "  "+l)
			}
			lines = append(lines, "", "  "+c("mute", "scan it with your phone, or open ")+c("task", w.link),
				"  "+c("mute", "the link works once, for 10 minutes · waiting for you to tap Start ")+c("hub", spinner(frame)))
		}
		lines = append(lines, "", "  "+key("p", "new link")+key("d d", "disconnect")+esc)
	default:
		level := "questions, plus runs' news and when they finish"
		if st.Level == "questions" {
			level = "only questions for you"
		}
		lines = append(lines, "  "+c("reply!", "✓ ")+c("hi!", "@"+st.BotUsername)+c("mute", " talks to ")+c("hi!", "@"+nonEmpty(st.ChatUsername, "you")),
			"  "+c("mute", "sends  ")+c("fg", level))
		if st.MutedUntil != nil {
			lines = append(lines, "  "+c("mute", "muted until "+st.MutedUntil.Local().Format("15:04")+" (/unmute in Telegram)"))
		}
		if w.link != "" {
			lines = append(lines, "", "  "+c("mute", "to pair another chat, open ")+c("task", w.link))
		}
		lines = append(lines, "", "  "+key("t", "send a test")+key("l", "switch what it sends")+key("p", "pair another chat")+key("d d", "disconnect")+esc)
	}
	if w.err != "" {
		lines = append(lines, "")
		lines = append(lines, say("fail", w.err)...)
	}
	if w.note != "" {
		lines = append(lines, "")
		lines = append(lines, say("reply", w.note)...)
	}
	return append(lines, "")
}

func spinner(frame int) string {
	const s = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	r := []rune(s)
	return string(r[(frame/2)%len(r)])
}

var (
	qrInk   = lipgloss.Color("#000000")
	qrPaper = lipgloss.Color("#ffffff")
	qrCells = map[[2]bool]string{}
)

func init() {
	for _, top := range []bool{false, true} {
		for _, bottom := range []bool{false, true} {
			fg, bg := qrPaper, qrPaper
			if top {
				fg = qrInk
			}
			if bottom {
				bg = qrInk
			}
			qrCells[[2]bool{top, bottom}] = lipgloss.NewStyle().Foreground(fg).Background(bg).Render("▀")
		}
	}
}

// QR draws text as a QR code, two modules a line, dark on light whatever the
// terminal's colors, with a quiet zone so phones read it.
func QR(text string) []string {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return nil
	}
	const quiet = 2
	n := code.Size + 2*quiet
	black := func(x, y int) bool {
		x, y = x-quiet, y-quiet
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	var out []string
	for y := 0; y < n; y += 2 {
		var b strings.Builder
		for x := range n {
			b.WriteString(qrCells[[2]bool{black(x, y), black(x, y+1)}])
		}
		out = append(out, b.String())
	}
	return out
}
