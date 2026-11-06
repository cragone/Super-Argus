package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// State persists read positions so a restart neither replays nor skips lines.
type State struct {
	path string

	mu      sync.Mutex
	Offsets map[string]int64 `json:"offsets,omitempty"` // file path -> bytes consumed
	Cursor  string           `json:"cursor,omitempty"`  // journald cursor
	dirty   bool
}

// LoadState reads the state file; a missing or unreadable file starts empty.
func LoadState(path string) *State {
	s := &State{path: path, Offsets: map[string]int64{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, s)
		if s.Offsets == nil {
			s.Offsets = map[string]int64{}
		}
	}
	return s
}

func (s *State) Offset(path string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	off, ok := s.Offsets[path]
	return off, ok
}

func (s *State) SetOffset(path string, off int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Offsets[path] != off {
		s.Offsets[path] = off
		s.dirty = true
	}
}

func (s *State) Forget(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Offsets[path]; ok {
		delete(s.Offsets, path)
		s.dirty = true
	}
}

func (s *State) SetCursor(c string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c != "" && s.Cursor != c {
		s.Cursor = c
		s.dirty = true
	}
}

func (s *State) GetCursor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cursor
}

// Save writes the state atomically if anything changed.
func (s *State) Save() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	b, err := json.Marshal(s)
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
