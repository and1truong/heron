// Package background manages per-user Heron services without enabling login startup.
package background

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Spec struct {
	Config      string
	Directory   string
	Executable  string
	Environment []string
	Token       string
}

type RuntimeState struct {
	Token     string
	PID       int
	State     string
	URL       string
	StartedAt time.Time
	Error     string
}

type Instance struct {
	ID, Dir, Config string
}

// Keep the final path component stable even when the YAML is removed or replaced.
// Directory symlinks are resolved, but a symlink used as the config filename is
// its own selection; use that same -c value for subsequent lifecycle commands.
func Locate(path string) (Instance, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Instance{}, err
		}
		path = filepath.Join(home, path[2:])
	}
	if path == "" {
		return Instance{}, fmt.Errorf("empty configuration path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Instance{}, err
	}
	if parent, e := filepath.EvalSymlinks(filepath.Dir(abs)); e == nil {
		abs = filepath.Join(parent, filepath.Base(abs))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Instance{}, err
	}
	hash := sha256.Sum256([]byte(abs))
	id := "heron-" + hex.EncodeToString(hash[:16])
	return Instance{ID: id, Dir: filepath.Join(home, ".local", "state", "heron", id), Config: abs}, nil
}

func (i Instance) SpecPath() string  { return filepath.Join(i.Dir, "spec.json") }
func (i Instance) LogPath() string   { return filepath.Join(i.Dir, "service.log") }
func (i Instance) StatePath() string { return filepath.Join(i.Dir, "runtime.json") }

func (i Instance) Prepare() error { return os.MkdirAll(i.Dir, 0700) }

func NewSpec(i Instance) (Spec, error) {
	executable, err := os.Executable()
	if err != nil {
		return Spec{}, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return Spec{}, err
	}
	dir, err := os.Getwd()
	if err != nil {
		return Spec{}, err
	}
	return Spec{Config: i.Config, Directory: dir, Executable: executable, Environment: os.Environ()}, nil
}

func (s *Spec) NewToken() error {
	var token [24]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	s.Token = hex.EncodeToString(token[:])
	return nil
}

func ReadJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func WriteJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, data)
}

// Readers must never see a partially written launch spec or readiness record.
func WriteFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
