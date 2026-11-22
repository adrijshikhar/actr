package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	saltLen    = 16
	nonceLen   = 12
	argonTime  = 1
	argonMem   = 64 * 1024
	argonThr   = 4
	argonKeyLn = 32
)

var _ Provider = (*FileVaultProvider)(nil)

type FileVaultProvider struct {
	path       string
	passphrase []byte
	mu         sync.RWMutex
	cache      map[string]string
}

type vaultPayload struct {
	Secrets   map[string]string `json:"secrets"`
	UpdatedAt string            `json:"updatedAt"`
}

// NewFileProvider returns an AES-256-GCM + Argon2id encrypted file vault provider.
// If the vault file already exists at vaultPath, it is decrypted and loaded into memory.
func NewFileProvider(vaultPath string, passphrase []byte) (*FileVaultProvider, error) {
	p := &FileVaultProvider{
		path:       vaultPath,
		passphrase: append([]byte(nil), passphrase...),
		cache:      make(map[string]string),
	}

	if _, err := os.Stat(vaultPath); err == nil {
		if err := p.load(); err != nil {
			return nil, fmt.Errorf("load encrypted vault: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat vault file: %w", err)
	}

	return p, nil
}

// Name returns the provider identifier.
func (p *FileVaultProvider) Name() string {
	return "encrypted-file"
}

func (p *FileVaultProvider) load() error {
	raw, err := os.ReadFile(p.path)
	if err != nil {
		return fmt.Errorf("read vault file: %w", err)
	}
	if len(raw) < saltLen+nonceLen {
		return errors.New("encrypted vault file corrupted or truncated")
	}

	salt := raw[:saltLen]
	nonce := raw[saltLen : saltLen+nonceLen]
	ciphertext := raw[saltLen+nonceLen:]

	key := argon2.IDKey(p.passphrase, salt, argonTime, argonMem, argonThr, argonKeyLn)

	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("create gcm: %w", err)
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return fmt.Errorf("decrypt vault payload: %w", err)
	}

	var payload vaultPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return fmt.Errorf("unmarshal vault payload: %w", err)
	}

	if payload.Secrets == nil {
		p.cache = make(map[string]string)
	} else {
		p.cache = payload.Secrets
	}
	return nil
}

func (p *FileVaultProvider) save() error {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}

	key := argon2.IDKey(p.passphrase, salt, argonTime, argonMem, argonThr, argonKeyLn)
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("create gcm: %w", err)
	}

	payload := vaultPayload{
		Secrets:   p.cache,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal vault payload: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	output := make([]byte, 0, len(salt)+len(nonce)+len(ciphertext))
	output = append(output, salt...)
	output = append(output, nonce...)
	output = append(output, ciphertext...)

	dir := filepath.Dir(p.path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create vault directory: %w", err)
		}
	}

	tmpFile := p.path + ".tmp"
	if err := os.WriteFile(tmpFile, output, 0o600); err != nil {
		return fmt.Errorf("write vault temp file: %w", err)
	}

	if err := os.Rename(tmpFile, p.path); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("rename vault file: %w", err)
	}

	return nil
}

// Get retrieves a secret by key from the in-memory cache.
func (p *FileVaultProvider) Get(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if val, ok := p.cache[key]; ok {
		return val, nil
	}
	return "", ErrSecretNotFound
}

// Set stores a secret by key, updates the in-memory cache, and atomically persists it to disk.
func (p *FileVaultProvider) Set(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	oldVal, existed := p.cache[key]
	p.cache[key] = value

	if err := p.save(); err != nil {
		if existed {
			p.cache[key] = oldVal
		} else {
			delete(p.cache, key)
		}
		return err
	}
	return nil
}

// Delete removes a secret by key from cache and atomically updates disk persistence.
func (p *FileVaultProvider) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	val, ok := p.cache[key]
	if !ok {
		return ErrSecretNotFound
	}

	delete(p.cache, key)
	if err := p.save(); err != nil {
		p.cache[key] = val
		return err
	}
	return nil
}

// List returns metadata for all secrets currently stored in the file vault.
func (p *FileVaultProvider) List(ctx context.Context) ([]SecretMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	list := make([]SecretMetadata, 0, len(p.cache))
	for k := range p.cache {
		list = append(list, SecretMetadata{Key: k, Provider: p.Name()})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Key < list[j].Key
	})
	return list, nil
}
