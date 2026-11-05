package chunk

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store persists chunk images by key. Implementations must be safe for
// concurrent use. A local-disk store ships today; S3 can implement the same interface.
type Store interface {
	Put(key string, data []byte) error
	Get(key string) ([]byte, error)
	// Delete removes the object; deleting a missing key is not an error.
	Delete(key string) error
}

// Key returns the relative storage key yyyy/mm/dd/<stream_id>/<chunk_id>.chk.
func Key(minTS int64, streamID uint64, chunkID string) string {
	t := time.Unix(0, minTS).UTC()
	return fmt.Sprintf("%04d/%02d/%02d/%d/%s.chk", t.Year(), int(t.Month()), t.Day(), streamID, chunkID)
}

// LocalStore stores chunks under a root directory.
type LocalStore struct{ root string }

// NewLocalStore returns a store rooted at dir.
func NewLocalStore(dir string) *LocalStore { return &LocalStore{root: filepath.Clean(dir)} }

func (s *LocalStore) path(key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") {
		return "", fmt.Errorf("chunk: invalid key %q", key)
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("chunk: invalid key %q", key)
		}
	}
	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

// Put writes data durably (temp file, fsync, rename).
func (s *LocalStore) Put(key string, data []byte) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Get reads a whole chunk.
func (s *LocalStore) Get(key string) ([]byte, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Delete removes a chunk and any now-empty parent directories.
func (s *LocalStore) Delete(key string) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for d := filepath.Dir(p); d != s.root && strings.HasPrefix(d, s.root); d = filepath.Dir(d) {
		if os.Remove(d) != nil { // non-empty or already gone
			break
		}
	}
	return nil
}
