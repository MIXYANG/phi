// Package shellhist stores project-wide history of what the user typed: shell
// commands (store.go) and submitted composer prompts (prompt.go). Callers
// supply the canonical sessions directory.
package shellhist

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxRecord  = 64 << 10
	tailBudget = 2 << 20
	maxEntries = 5000
	rotateSize = 8 << 20
)

// Entry is one user command. Version must be 1; a nil Exit means no exit status is available.
type Entry struct {
	Version int    `json:"v"`
	At      int64  `json:"at"`
	Cwd     string `json:"cwd"`
	Command string `json:"cmd"`
	Exit    *int   `json:"exit"`
}

// Store shares history.jsonl across sessions using the supplied directory.
// Its methods are safe for concurrent use.
type Store struct {
	dir     string
	mu      sync.Mutex
	current fileCache
	backup  fileCache
}

func New(dir string) *Store { return &Store{dir: dir} }

func validate(e Entry) error {
	if e.Version != 1 {
		return fmt.Errorf("shell history: unsupported version %d; use version 1", e.Version)
	}
	if strings.TrimSpace(e.Command) == "" {
		return errors.New("shell history: command must not be blank")
	}
	if !utf8.ValidString(e.Command) {
		return errors.New("shell history: command must be valid UTF-8")
	}
	return nil
}

// Append writes a single JSONL record. Oversized or invalid records are rejected.
func (s *Store) Append(e Entry) error {
	if err := validate(e); err != nil {
		return err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode shell history: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxRecord {
		return errors.New("shell history: encoded record exceeds 64 KiB; shorten the command or working directory")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// #nosec G703 -- The caller supplies the canonical project sessions directory.
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create shell history directory: %w", err)
	}
	// #nosec G703 -- Fixed filename under the caller's canonical sessions directory.
	lock, err := os.OpenFile(filepath.Join(s.dir, "history.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open shell history lock: %w", err)
	}
	defer lock.Close()
	if err := lockFile(lock); err != nil {
		return fmt.Errorf("lock shell history: %w", err)
	}
	defer unlockFile(lock)
	return s.appendRecord(data)
}

func (s *Store) appendRecord(data []byte) error {
	path := filepath.Join(s.dir, "history.jsonl")
	// #nosec G703 -- Fixed filename under the caller's canonical sessions directory.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open shell history: %w", err)
	}
	defer func() {
		if f != nil {
			_ = f.Close()
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat shell history: %w", err)
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
			return fmt.Errorf("read shell history tail: %w", err)
		}
		// Separate a crashed writer's partial record from the next command.
		if last[0] != '\n' {
			data = append([]byte{'\n'}, data...)
		}
	}
	if info.Size()+int64(len(data)) > rotateSize {
		if err := f.Close(); err != nil {
			return fmt.Errorf("close shell history before rotation: %w", err)
		}
		backup := filepath.Join(s.dir, "history.1.jsonl")
		// #nosec G703 -- Only the fixed backup filename is removed.
		if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove previous shell history backup: %w", err)
		}
		// #nosec G703 -- Both paths are fixed history filenames.
		if err := os.Rename(path, backup); err != nil {
			return fmt.Errorf("rotate shell history: %w", err)
		}
		// #nosec G703 -- Recreate the fixed history filename after rotation.
		f, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("create shell history after rotation: %w", err)
		}
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("append shell history: %w", err)
	}
	return nil
}
