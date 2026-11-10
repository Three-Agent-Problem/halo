package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Images in prompts. The agents run on this machine and Claude Code's Read tool
// opens images, so an image goes to them as a path: Halo copies it into its
// image directory (screenshots in temp folders vanish) and the text carries
// "(image: /abs/path.png …)". A chip like [img1 shot.png] stands for it while
// you type.

var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

// imagePath returns the image file a paste names, or "". Dragging a file into
// the terminal pastes its path: maybe quoted, a file:// URL, or with
// backslash-escaped spaces.
func imagePath(paste string) string {
	p := strings.TrimSpace(paste)
	if len(p) >= 2 && (p[0] == '\'' || p[0] == '"') && p[len(p)-1] == p[0] {
		p = p[1 : len(p)-1]
	}
	if strings.HasPrefix(p, "file://") {
		u, err := url.Parse(p)
		if err != nil {
			return ""
		}
		p = u.Path
	}
	p = strings.ReplaceAll(p, `\ `, " ")
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, rest)
		}
	}
	if !filepath.IsAbs(p) || !imageExt[strings.ToLower(filepath.Ext(p))] {
		return ""
	}
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return p
}

// imageDir is where Halo keeps the images it sends.
func (m *model) imageDir() string {
	if m.o.ImageDir != "" {
		return m.o.ImageDir
	}
	return filepath.Join(os.TempDir(), "halo-images")
}

// attach copies image src into the image directory and types its chip.
func (m *model) attach(src string) error {
	dir := m.imageDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, time.Now().Format("20060102-150405.000")+"-"+filepath.Base(src))
	if err := copyFile(src, dst); err != nil {
		return err
	}
	m.chip(dst, filepath.Base(src))
	return nil
}

// chip types the placeholder of image path into the input.
func (m *model) chip(path, name string) {
	if m.images == nil {
		m.images = map[string]string{}
	}
	chip := fmt.Sprintf("[img%d %s]", len(m.images)+1, name)
	m.images[chip] = path
	m.input.InsertString(chip + " ")
	m.popSel, m.popOff = 0, false
}

// expandImages turns the chips in text into the paths the agents read, and
// returns the images it names, which are attached too.
func (m *model) expandImages(text string) (string, []string) {
	var used []string
	for chip, path := range m.images {
		if strings.Contains(text, chip) {
			used = append(used, path)
			text = strings.ReplaceAll(text, chip, "(image: "+path+"; open it with the Read tool)")
		}
	}
	sort.Strings(used)
	return text, used
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type clipMsg struct {
	path string
	err  error
	key  tea.KeyPressMsg // the ctrl+v, for a text paste when there is no image
}

var errNoImage = errors.New("no image on the clipboard")

// clipImage saves the clipboard's image (a screenshot copied with
// cmd+ctrl+shift+4, say) into the image directory.
func (m *model) clipImage(k tea.KeyPressMsg) tea.Cmd {
	dir := m.imageDir()
	return func() tea.Msg {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return clipMsg{err: err, key: k}
		}
		dst := filepath.Join(dir, time.Now().Format("20060102-150405.000")+"-clipboard.png")
		return clipMsg{path: dst, err: saveClipboardPNG(dst), key: k}
	}
}

func saveClipboardPNG(dst string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`set f to open for access POSIX file %q with write permission
write (the clipboard as «class PNGf») to f
close access f`, dst)
		cmd = exec.CommandContext(ctx, "osascript", "-e", script)
	case "linux":
		if _, err := exec.LookPath("wl-paste"); err == nil {
			cmd = exec.CommandContext(ctx, "wl-paste", "--type", "image/png")
		} else {
			cmd = exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", "image/png", "-o")
		}
	default:
		return errNoImage
	}
	var out bytes.Buffer
	if runtime.GOOS != "darwin" {
		cmd.Stdout = &out
	}
	if err := cmd.Run(); err != nil {
		_ = os.Remove(dst)
		return errNoImage
	}
	if runtime.GOOS != "darwin" {
		if !bytes.HasPrefix(out.Bytes(), []byte("\x89PNG")) {
			return errNoImage
		}
		return os.WriteFile(dst, out.Bytes(), 0o644)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Size() == 0 {
		_ = os.Remove(dst)
		return errNoImage
	}
	return nil
}
