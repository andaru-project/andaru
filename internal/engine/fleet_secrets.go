// SPDX-License-Identifier: AGPL-3.0-or-later
package engine

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// fleetSecrets stores per-target polling credentials encrypted at rest with AES-256-GCM.
type fleetSecrets struct {
	mu      sync.RWMutex
	keyPath string
	encPath string
	aead    cipher.AEAD
	data    map[string]map[string]string // targetID -> secretKey -> secretValue
}

func openFleetSecrets(stateDir string) (*fleetSecrets, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create fleet state dir: %w", err)
	}
	fs := &fleetSecrets{
		keyPath: filepath.Join(stateDir, "fleet.key"),
		encPath: filepath.Join(stateDir, "fleet-secrets.enc"),
		data:    map[string]map[string]string{},
	}
	key, err := fs.loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init fleet secret cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("init fleet secret gcm: %w", err)
	}
	fs.aead = aead
	if err := fs.load(); err != nil {
		return nil, err
	}
	return fs, nil
}

func (fs *fleetSecrets) loadOrCreateKey() ([]byte, error) {
	raw, err := os.ReadFile(fs.keyPath)
	if err == nil {
		if len(raw) != 32 {
			return nil, fmt.Errorf("invalid fleet secret key length %d", len(raw))
		}
		return raw, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read fleet secret key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate fleet secret key: %w", err)
	}
	if err := writeFileAtomic(fs.keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("write fleet secret key: %w", err)
	}
	return key, nil
}

func (fs *fleetSecrets) load() error {
	raw, err := os.ReadFile(fs.encPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read fleet secrets: %w", err)
	}
	ns := fs.aead.NonceSize()
	if len(raw) < ns {
		return errors.New("corrupt fleet secrets ciphertext")
	}
	plain, err := fs.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return fmt.Errorf("decrypt fleet secrets: %w", err)
	}
	var decoded map[string]map[string]string
	if err := json.Unmarshal(plain, &decoded); err != nil {
		return fmt.Errorf("decode fleet secrets: %w", err)
	}
	if decoded != nil {
		fs.data = decoded
	}
	return nil
}

func (fs *fleetSecrets) saveLocked() error {
	plain, err := json.Marshal(fs.data)
	if err != nil {
		return fmt.Errorf("encode fleet secrets: %w", err)
	}
	nonce := make([]byte, fs.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generate fleet secret nonce: %w", err)
	}
	ciphertext := fs.aead.Seal(nonce, nonce, plain, nil)
	return writeFileAtomic(fs.encPath, ciphertext, 0o600)
}

func (fs *fleetSecrets) Get(targetID string) map[string]string {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	src := fs.data[targetID]
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (fs *fleetSecrets) Set(targetID string, secrets map[string]string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(secrets) == 0 {
		delete(fs.data, targetID)
	} else {
		cp := make(map[string]string, len(secrets))
		for k, v := range secrets {
			if k != "" && v != "" {
				cp[k] = v
			}
		}
		if len(cp) == 0 {
			delete(fs.data, targetID)
		} else {
			fs.data[targetID] = cp
		}
	}
	return fs.saveLocked()
}

func (fs *fleetSecrets) Delete(targetID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.data[targetID]; !ok {
		return nil
	}
	delete(fs.data, targetID)
	return fs.saveLocked()
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".fleet-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
