package relay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// VAPIDStore loads or generates a VAPID keypair and persists it to disk so the
// same keypair survives relay restarts. The private key is written with mode
// 0600 and must never be sent to the phone. The public key is safe to
// distribute to push subscribers.
type VAPIDStore struct {
	privPath string
	pubPath  string

	mu         sync.RWMutex
	privateKey string // URL-safe base64 ECDSA private key
	publicKey  string // URL-safe base64 ECDSA public key (uncompressed)
}

// NewVAPIDStore creates a VAPIDStore that keeps its keypair files under dir.
// The private key is stored at <dir>/vapid-private.pem (mode 0600) and
// the public key at <dir>/vapid-public.txt (mode 0644).
// Call LoadOrGenerate before accessing keys.
func NewVAPIDStore(dir string) *VAPIDStore {
	return &VAPIDStore{
		privPath: filepath.Join(dir, "vapid-private.pem"),
		pubPath:  filepath.Join(dir, "vapid-public.txt"),
	}
}

// PublicKey returns the URL-safe base64-encoded VAPID public key.
// Returns an empty string before LoadOrGenerate has been called.
func (v *VAPIDStore) PublicKey() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.publicKey
}

// PrivateKey returns the URL-safe base64-encoded VAPID private key.
// Returns an empty string before LoadOrGenerate has been called.
// The private key must never be sent to the phone or included in any HTTP response.
func (v *VAPIDStore) PrivateKey() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.privateKey
}

// LoadOrGenerate returns the VAPID private and public keys. On first call with
// a fresh directory the keys are generated and persisted to disk; on subsequent
// calls the existing files are read back unchanged. The returned keys are also
// cached in memory and accessible via PublicKey() and PrivateKey().
func (v *VAPIDStore) LoadOrGenerate() (privateKey, publicKey string, err error) {
	privData, privErr := os.ReadFile(v.privPath)
	pubData, pubErr := os.ReadFile(v.pubPath)

	if privErr == nil && pubErr == nil {
		priv := strings.TrimSpace(string(privData))
		pub := strings.TrimSpace(string(pubData))
		if priv != "" && pub != "" {
			v.mu.Lock()
			v.privateKey = priv
			v.publicKey = pub
			v.mu.Unlock()
			return priv, pub, nil
		}
	}

	// One or both files are missing or empty — generate a fresh pair.
	if privErr != nil && !errors.Is(privErr, os.ErrNotExist) {
		return "", "", privErr
	}
	if pubErr != nil && !errors.Is(pubErr, os.ErrNotExist) {
		return "", "", pubErr
	}

	privKey, pubKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", err
	}

	if err := os.MkdirAll(filepath.Dir(v.privPath), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(v.privPath, []byte(privKey), 0o600); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(v.pubPath), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(v.pubPath, []byte(pubKey), 0o644); err != nil { //nolint:gosec
		return "", "", err
	}

	v.mu.Lock()
	v.privateKey = privKey
	v.publicKey = pubKey
	v.mu.Unlock()
	return privKey, pubKey, nil
}
