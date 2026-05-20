// Package crypto implements AES-256-GCM envelope encryption with per-tenant
// DEKs derived from a versioned master-key map.
//
// At-rest layout for an encrypted blob: nonce(12) || gcm_seal(dek, plaintext).
// The DEK is never persisted; only the master-key version label is stored
// alongside the ciphertext so that the correct master can be looked up at
// decrypt time.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/crypto/hkdf"
)

const (
	masterKeyLen = 32
	nonceLen     = 12
	dekLen       = 32

	envMasterKey     = "CHAT_BOT_MASTER_KEY"
	envMasterKeyFile = "CHAT_BOT_MASTER_KEY_FILE"

	// saltPrefix is concatenated with sha256(tenantID) to form the HKDF salt.
	// Versioning it ("v1") lets us migrate the derivation scheme without
	// losing the ability to decrypt older ciphertexts: if we ever introduce
	// "moses-chat-bot.v2", existing rows still decrypt because they encode
	// their derivation lineage in the master-key version label, not the salt.
	saltPrefix = "moses-chat-bot.v1"

	// info binds the derived key to the specific use case ("api-key/v1").
	// Reusing the same master for a different purpose must use a different
	// info string so domain separation holds.
	infoAPIKeyV1 = "api-key/v1"
)

var (
	ErrMissingMasterKey     = errors.New("crypto: no master keys configured")
	ErrInvalidMasterKeys    = errors.New("crypto: invalid master keys (bad length, missing active, etc.)")
	ErrMasterKeyUnavailable = errors.New("crypto: referenced master key version not loaded")
	ErrEmptyPlaintext       = errors.New("crypto: empty plaintext not allowed")
	ErrInvalidCiphertext    = errors.New("crypto: ciphertext too short or malformed")
)

// MasterKeys is the in-memory representation of the master-key Secret.
//
// The on-disk JSON form encodes Keys as a map[string]string of base64
// values; LoadMasterKeysFromEnv handles the decode.
type MasterKeys struct {
	Keys   map[string][]byte `json:"-"`
	Active string            `json:"active"`
}

// masterKeysJSON mirrors the on-disk JSON shape used for loading.
type masterKeysJSON struct {
	Keys   map[string]string `json:"keys"`
	Active string            `json:"active"`
}

// Envelope encrypts/decrypts data under per-tenant DEKs derived from a
// versioned master-key map. Safe for concurrent use; SetMasterKeys swaps
// the underlying keys atomically.
type Envelope struct {
	mu sync.RWMutex
	mk *MasterKeys
}

// LoadMasterKeysFromEnv reads master keys from the CHAT_BOT_MASTER_KEY env
// var (containing the JSON directly) or, if empty, from the file at
// CHAT_BOT_MASTER_KEY_FILE. The JSON shape is:
//
//	{"keys": {"v1": "<base64-32-bytes>", ...}, "active": "v1"}
//
// Values are base64-decoded into raw bytes.
func LoadMasterKeysFromEnv() (*MasterKeys, error) {
	raw := []byte(os.Getenv(envMasterKey))
	if len(raw) == 0 {
		path := os.Getenv(envMasterKeyFile)
		if path == "" {
			return nil, ErrMissingMasterKey
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("crypto: reading master-key file: %w", err)
		}
		raw = b
	}

	var parsed masterKeysJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMasterKeys, err)
	}
	if len(parsed.Keys) == 0 {
		return nil, fmt.Errorf("%w: keys map empty", ErrInvalidMasterKeys)
	}
	if parsed.Active == "" {
		return nil, fmt.Errorf("%w: active missing", ErrInvalidMasterKeys)
	}

	decoded := make(map[string][]byte, len(parsed.Keys))
	for id, b64 := range parsed.Keys {
		k, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("%w: key %q base64 decode: %v", ErrInvalidMasterKeys, id, err)
		}
		decoded[id] = k
	}

	return &MasterKeys{Keys: decoded, Active: parsed.Active}, nil
}

// NewEnvelope validates the master keys and returns a ready-to-use Envelope.
// All keys must be exactly 32 bytes and the active label must be present.
func NewEnvelope(mk *MasterKeys) (*Envelope, error) {
	if err := validateMasterKeys(mk); err != nil {
		return nil, err
	}
	return &Envelope{mk: cloneMasterKeys(mk)}, nil
}

func validateMasterKeys(mk *MasterKeys) error {
	if mk == nil {
		return fmt.Errorf("%w: nil master keys", ErrInvalidMasterKeys)
	}
	if len(mk.Keys) == 0 {
		return fmt.Errorf("%w: keys map empty", ErrInvalidMasterKeys)
	}
	if mk.Active == "" {
		return fmt.Errorf("%w: active label empty", ErrInvalidMasterKeys)
	}
	if _, ok := mk.Keys[mk.Active]; !ok {
		return fmt.Errorf("%w: active label %q not in keys", ErrInvalidMasterKeys, mk.Active)
	}
	for id, k := range mk.Keys {
		if len(k) != masterKeyLen {
			return fmt.Errorf("%w: key %q has length %d, want %d", ErrInvalidMasterKeys, id, len(k), masterKeyLen)
		}
	}
	return nil
}

func cloneMasterKeys(mk *MasterKeys) *MasterKeys {
	out := &MasterKeys{
		Keys:   make(map[string][]byte, len(mk.Keys)),
		Active: mk.Active,
	}
	for id, k := range mk.Keys {
		buf := make([]byte, len(k))
		copy(buf, k)
		out.Keys[id] = buf
	}
	return out
}

// SetMasterKeys atomically swaps in a new set of master keys (SIGHUP-style
// reload). Returns an error if validation fails; existing keys are left
// untouched in that case.
func (e *Envelope) SetMasterKeys(mk *MasterKeys) error {
	if err := validateMasterKeys(mk); err != nil {
		return err
	}
	cloned := cloneMasterKeys(mk)
	e.mu.Lock()
	e.mk = cloned
	e.mu.Unlock()
	return nil
}

// ActiveKeyID returns the current write-side key version label.
func (e *Envelope) ActiveKeyID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mk.Active
}

// Encrypt seals plaintext under a DEK derived from the active master key and
// the given tenantID. Returns the ciphertext, the master-key version label
// that was used, and any error.
func (e *Envelope) Encrypt(tenantID uuid.UUID, plaintext []byte) ([]byte, string, error) {
	if len(plaintext) == 0 {
		return nil, "", ErrEmptyPlaintext
	}

	e.mu.RLock()
	keyID := e.mk.Active
	master, ok := e.mk.Keys[keyID]
	if !ok {
		e.mu.RUnlock()
		return nil, "", ErrMasterKeyUnavailable
	}
	masterCopy := make([]byte, len(master))
	copy(masterCopy, master)
	e.mu.RUnlock()

	dek, err := deriveDEK(masterCopy, tenantID)
	if err != nil {
		return nil, "", err
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, "", fmt.Errorf("crypto: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", fmt.Errorf("crypto: cipher.NewGCM: %w", err)
	}

	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, "", fmt.Errorf("crypto: nonce: %w", err)
	}

	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, nonceLen+len(sealed))
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, keyID, nil
}

// Decrypt opens ciphertext using the master key identified by keyID and a
// DEK derived for tenantID. Returns ErrMasterKeyUnavailable if keyID is not
// loaded, ErrInvalidCiphertext if too short, and a generic error from the
// GCM layer on auth failure (tamper, wrong tenant, wrong key).
func (e *Envelope) Decrypt(tenantID uuid.UUID, ciphertext []byte, keyID string) ([]byte, error) {
	if len(ciphertext) < nonceLen {
		return nil, ErrInvalidCiphertext
	}

	e.mu.RLock()
	master, ok := e.mk.Keys[keyID]
	if !ok {
		e.mu.RUnlock()
		return nil, ErrMasterKeyUnavailable
	}
	masterCopy := make([]byte, len(master))
	copy(masterCopy, master)
	e.mu.RUnlock()

	dek, err := deriveDEK(masterCopy, tenantID)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("crypto: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher.NewGCM: %w", err)
	}

	nonce := ciphertext[:nonceLen]
	sealed := ciphertext[nonceLen:]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: gcm open: %w", err)
	}
	return plaintext, nil
}

// deriveDEK runs HKDF-SHA256 over the master key with a tenant-bound salt.
// Salt construction: saltPrefix || sha256(tenantID[:]). This guarantees:
//   - Domain separation between tenants (two tenants can never derive the
//     same DEK even under the same master).
//   - A stable derivation function across deploys (no random salt → no need
//     to persist the salt alongside the ciphertext).
//   - Forward-compatibility: bumping the salt prefix to "moses-chat-bot.v2"
//     would force re-derivation without needing schema changes.
func deriveDEK(master []byte, tenantID uuid.UUID) ([]byte, error) {
	tenantDigest := sha256.Sum256(tenantID[:])
	salt := make([]byte, 0, len(saltPrefix)+len(tenantDigest))
	salt = append(salt, []byte(saltPrefix)...)
	salt = append(salt, tenantDigest[:]...)

	r := hkdf.New(sha256.New, master, salt, []byte(infoAPIKeyV1))
	dek := make([]byte, dekLen)
	if _, err := io.ReadFull(r, dek); err != nil {
		return nil, fmt.Errorf("crypto: hkdf read: %w", err)
	}
	return dek, nil
}
