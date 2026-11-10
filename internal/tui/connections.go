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

// connWizard is the /connections dialog.
type connWizard struct {
	st    *client.Telegram
	token string
	link  string
	busy  bool
	err   string
	note  string
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
	m.conn = &connWizard{busy: true}
	return m.tgDo(func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.Telegram(ctx) }, "", false)
}

// tgDo runs a Connector call in the background.
func (m *model) tgDo(f func(context.Context, Connector) (*client.Telegram, error), note string, poll bool) tea.Cmd {
	c := m.connector()
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
		defer cancel()
		st, err := f(ctx, c)
		return tgMsg{st: st, err: err, note: note, poll: poll}
	}
}

func tgPoll() tea.Cmd { return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tgPollMsg{} }) }

// tgUpdate handles the dialog's background results.
func (m *model) tgUpdate(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tgMsg:
		if msg.st != nil && msg.err == nil {
			m.tgPaired = msg.st.Paired
		}
		w := m.conn
		if w == nil {
			return nil
		}
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
		if msg.st.Paired && !wasPaired && w.st != nil && msg.poll {
			w.link, w.note = "", "✓ paired. Questions for you will arrive in Telegram too."
		} else if msg.note != "" {
			w.note = msg.note
		}
		if msg.st.Connected && !msg.st.Paired && w.link != "" {
			return tgPoll()
		}
	case tgPollMsg:
		if m.conn == nil || m.conn.st == nil || m.conn.st.Paired {
			return nil
		}
		return m.tgDo(func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.Telegram(ctx) }, "", true)
	}
	return nil
}

// connKey handles a key while the dialog is open.
func (m *model) connKey(k tea.KeyPressMsg) tea.Cmd {
	w, key := m.conn, k.String()
	if key == "esc" {
		m.conn = nil
		return nil
	}
	if w.busy {
		return nil
	}
	w.err, w.note = "", ""
	switch {
	case w.st == nil:
		return nil
	case !w.st.Connected:
		switch key {
		case "enter":
			token := strings.TrimSpace(w.token)
			if token == "" {
				return nil
			}
			w.busy, w.token = true, ""
			return m.tgDo(func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.ConnectTelegram(ctx, token) }, "", false)
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
			return m.tgDo(func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.PairTelegram(ctx) }, "", false)
		case "l":
			if !w.st.Paired {
				return nil
			}
			next := "questions"
			if w.st.Level == "questions" {
				next = "milestones"
			}
			w.busy = true
			return m.tgDo(func(ctx context.Context, c Connector) (*client.Telegram, error) { return c.SetTelegramLevel(ctx, next) }, "saved", false)
		case "t":
			if !w.st.Paired {
				return nil
			}
			w.busy = true
			return m.tgDo(func(ctx context.Context, c Connector) (*client.Telegram, error) {
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
	if w := m.conn; w != nil && w.st != nil && !w.st.Connected {
		w.token += strings.TrimSpace(s)
	}
}

// connBox draws the dialog.
func (m *model) connBox(w int) []string {
	cw := m.conn
	inner := w - 6
	say := func(style, s string) []string {
		var out []string
		for _, l := range strings.Split(ansi.Wordwrap(s, inner, ""), "\n") {
			out = append(out, "  "+c(style, l))
		}
		return out
	}
	key := func(k, what string) string { return c("reply!", k) + c("mute", " "+what+"   ") }
	lines := []string{""}
	lines = append(lines, say("fg", "Answer Halo's questions and follow its runs from your phone, through your own Telegram bot on Lagrange.")...)
	lines = append(lines, say("mute", "The bot only answers with buttons: approve, reject, pick an option. It can't start, stop or message anything, and typed text goes nowhere.")...)
	lines = append(lines, "")
	switch st := cw.st; {
	case st == nil:
		lines = append(lines, "  "+c("mute", "reading the connection…"))
	case !st.Connected:
		lines = append(lines,
			"  "+c("hub!", "1")+c("fg", "  In Telegram, open ")+c("hi!", "@BotFather")+c("fg", " and send ")+c("hi!", "/newbot"),
			"  "+c("hub!", "2")+c("fg", "  Pick a name, then a username that ends in “bot”"),
			"  "+c("hub!", "3")+c("fg", "  Paste the token it sends you here"),
			"")
		masked := strings.Repeat("•", min(len([]rune(cw.token)), inner-12))
		cursor := c("reply!", "▏")
		if cw.busy {
			cursor = c("mute", " checking with Telegram…")
		}
		lines = append(lines, "  "+c("mute", "token  ")+c("hi", masked)+cursor, "", "  "+key("enter", "connect")+key("esc", "close"))
	case !st.Paired:
		lines = append(lines, say("fg", "Connected to @"+st.BotUsername+". Now open it and tap Start: that pairs it with your chat.")...)
		if cw.link != "" {
			lines = append(lines, "")
			for _, l := range QR(cw.link) {
				lines = append(lines, "  "+l)
			}
			lines = append(lines, "", "  "+c("mute", "scan it with your phone, or open ")+c("task", cw.link),
				"  "+c("mute", "the link works once, for 10 minutes · waiting for you to tap Start ")+c("hub", spinner(m.frame)))
		}
		lines = append(lines, "", "  "+key("p", "new link")+key("esc", "close"))
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
		if cw.link != "" {
			lines = append(lines, "", "  "+c("mute", "to pair another chat, open ")+c("task", cw.link))
		}
		lines = append(lines, "", "  "+key("t", "send a test")+key("l", "switch what it sends")+key("p", "pair another chat")+key("esc", "close"))
	}
	if cw.err != "" {
		lines = append(lines, "")
		lines = append(lines, say("fail", cw.err)...)
	}
	if cw.note != "" {
		lines = append(lines, "")
		lines = append(lines, say("reply", cw.note)...)
	}
	lines = append(lines, "")
	return strings.Split(box(w, len(lines)+2, c("task!", "TELEGRAM"), "", lines, true), "\n")
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
