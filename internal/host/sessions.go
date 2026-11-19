package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/a2aproject/a2a-go/v2/integrations/executor/a2aacp"
)

// sessionFile maps keys to ACP sessions in a JSON file, so Claude sessions outlive
// the process: a restarted Halo asks the agent to session/load them.
type sessionFile struct {
	path string
	mu   sync.Mutex
	m    map[string]a2aacp.Session
}

func openSessions(path string) *sessionFile {
	f := &sessionFile{path: path, m: map[string]a2aacp.Session{}}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &f.m)
	}
	return f
}

func (f *sessionFile) get(key string) (a2aacp.Session, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.m[key]
	return s, ok, nil
}

func (f *sessionFile) put(key string, s a2aacp.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key] = s
	raw, err := json.MarshalIndent(f.m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}
