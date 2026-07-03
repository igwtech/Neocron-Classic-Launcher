package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Store persists a Session to disk between launches. The official launcher
// wraps the token with DPAPI on Windows and stores it unprotected under Wine
// (see docs/RE_LAUNCHER.md §5.2). We store 0600 JSON on all platforms; a
// Windows DPAPI wrap is a future hardening step and is transparent to callers.
type Store struct{ path string }

// NewStore returns a Store writing session.json under dir.
func NewStore(dir string) *Store {
	return &Store{path: filepath.Join(dir, "session.json")}
}

// Load reads the persisted session, or (nil, nil) if none exists.
func (s *Store) Load() (*Session, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, err
	}
	if sess.Token == "" {
		return nil, nil
	}
	return &sess, nil
}

// Save writes the session with owner-only permissions.
func (s *Store) Save(sess *Session) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

// Clear removes the persisted session (sign-out).
func (s *Store) Clear() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
