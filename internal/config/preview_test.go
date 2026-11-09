package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectWeb(t *testing.T) {
	for _, tc := range []struct {
		name, file, body, want string
		web                    bool
	}{
		{"dev script", "package.json", `{"scripts":{"dev":"vite","start":"x"}}`, "npm run dev", true},
		{"start script", "package.json", `{"scripts":{"start":"node ."}}`, "npm start", true},
		{"no scripts", "package.json", `{"scripts":{"test":"jest"}}`, "", false},
		{"bad json", "package.json", `{`, "", false},
		{"index.html", "index.html", "<html>", "npm run dev", true},
		{"vite config", "vite.config.ts", "", "npm run dev", true},
		{"next config", "next.config.mjs", "", "npm run dev", true},
		{"go project", "go.mod", "module x", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, tc.file, tc.body)
			got, web := DetectWeb(dir)
			if got != tc.want || web != tc.web {
				t.Errorf("DetectWeb = %q, %v; want %q, %v", got, web, tc.want, tc.web)
			}
		})
	}
}

func TestPreviewOverride(t *testing.T) {
	dir := t.TempDir()
	c := &Config{Preview: " pnpm dev --port 4000 "}
	if got, ok := c.PreviewCommand(dir); !ok || got != "pnpm dev --port 4000" {
		t.Errorf("override on a plain dir = %q, %v", got, ok)
	}
	write(t, dir, "package.json", `{"scripts":{"dev":"vite"}}`)
	if got, _ := c.PreviewCommand(dir); got != "pnpm dev --port 4000" {
		t.Errorf("override should beat detection, got %q", got)
	}
	if got, ok := (&Config{}).PreviewCommand(dir); !ok || got != "npm run dev" {
		t.Errorf("detection = %q, %v", got, ok)
	}
}

func TestPreviewYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "halo.yaml")
	write(t, dir, "halo.yaml", "preview: npm run serve\nagents:\n- {name: l, model: opus, role: lead}\n- {name: r, model: sonnet, role: reviewer}\n- {name: w, model: sonnet}\n")
	c, err := Load(path)
	if err != nil || c.Preview != "npm run serve" {
		t.Fatalf("Load: %v, preview %q", err, c.Preview)
	}
}
