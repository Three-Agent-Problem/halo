package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// PreviewCommand returns the command that serves the project in dir and whether
// it is a web project. The configured Preview wins; otherwise dir is a web
// project when its package.json has a dev or start script (dev preferred), or
// when it has an index.html or a vite/next config (then "npm run dev").
func (c *Config) PreviewCommand(dir string) (string, bool) {
	if c != nil && strings.TrimSpace(c.Preview) != "" {
		return strings.TrimSpace(c.Preview), true
	}
	return DetectWeb(dir)
}

// DetectWeb looks for the signs of a web project in dir.
func DetectWeb(dir string) (string, bool) {
	if raw, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(raw, &pkg) == nil {
			for _, name := range []string{"dev", "start"} {
				if _, ok := pkg.Scripts[name]; ok {
					if name == "dev" {
						return "npm run dev", true
					}
					return "npm start", true
				}
			}
		}
	}
	for _, f := range []string{"index.html", "vite.config.js", "vite.config.ts", "vite.config.mjs",
		"next.config.js", "next.config.mjs", "next.config.ts"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return "npm run dev", true
		}
	}
	return "", false
}
