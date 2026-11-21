package levelup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Intent struct {
	QQ     int64  `json:"qq"`
	PID    int64  `json:"pid"`
	Device int64  `json:"device"`
	Phase  string `json:"phase"`
	At     int64  `json:"at"`
}
type State struct {
	Profiles    map[string]string `json:"profiles"`
	Intents     map[string]Intent `json:"intents"`
	Seen        map[string]int64  `json:"seen"`
	LastBatch   map[string]int64  `json:"last_batch"`
	Initialized map[string]bool   `json:"initialized"`
}
type Store struct {
	mu    sync.Mutex
	state State
	dir   string
	aead  cipher.AEAD
}

func newState() State {
	return State{Profiles: map[string]string{}, Intents: map[string]Intent{}, Seen: map[string]int64{}, LastBatch: map[string]int64{}, Initialized: map[string]bool{}}
}
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "state.key")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if _, exists := os.Stat(filepath.Join(dir, "state.enc")); exists == nil {
			return nil, errors.New("STATE_KEY_MISSING")
		}
		b = make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			return nil, err
		}
		if err = atomicFile(path, b); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("STATE_KEY_INVALID")
	}
	block, err := aes.NewCipher(b)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, aead: aead, state: newState()}
	raw, err := os.ReadFile(filepath.Join(dir, "state.enc"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) < aead.NonceSize() {
		return nil, errors.New("STATE_CORRUPT")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte("laoluo-levelup-state-v1"))
	if err != nil {
		return nil, errors.New("STATE_CORRUPT")
	}
	if err = json.Unmarshal(plain, &s.state); err != nil {
		return nil, err
	}
	if s.state.Profiles == nil || s.state.Intents == nil || s.state.Seen == nil || s.state.LastBatch == nil || s.state.Initialized == nil {
		return nil, errors.New("STATE_SCHEMA")
	}
	return s, nil
}
func atomicFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".laoluo-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	return err
}
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(s.state)
	var copy State
	if err := json.Unmarshal(raw, &copy); err != nil {
		return err
	}
	if err := fn(&copy); err != nil {
		return err
	}
	raw, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	sealed := s.aead.Seal(nonce, nonce, raw, []byte("laoluo-levelup-state-v1"))
	if err = atomicFile(filepath.Join(s.dir, "state.enc"), sealed); err != nil {
		return err
	}
	s.state = copy
	return nil
}
func (s *Store) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(s.state)
	var copy State
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func (s *Store) Admit(eventKey string) bool {
	accepted := false
	digest := sha256.Sum256([]byte(eventKey))
	hash := hex.EncodeToString(digest[:])
	err := s.Update(func(st *State) error {
		at := time.Now().Unix()
		for k, t := range st.Seen {
			if at-t > 86400 {
				delete(st.Seen, k)
			}
		}
		if _, ok := st.Seen[hash]; ok {
			return nil
		}
		if len(st.Seen) >= 20000 {
			return errors.New("EVENT_CAPACITY")
		}
		st.Seen[hash] = at
		accepted = true
		return nil
	})
	return err == nil && accepted
}
