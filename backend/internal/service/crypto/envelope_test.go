package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeKey(b byte) []byte {
	k := make([]byte, masterKeyLen)
	for i := range k {
		k[i] = b
	}
	return k
}

func newTestEnvelope(t *testing.T, keys map[string][]byte, active string) *Envelope {
	t.Helper()
	mk := &MasterKeys{Keys: keys, Active: active}
	env, err := NewEnvelope(mk)
	require.NoError(t, err)
	return env
}

func TestEnvelope_RoundTrip(t *testing.T) {
	env := newTestEnvelope(t, map[string][]byte{"v1": makeKey(0x01)}, "v1")
	tenant := uuid.New()
	plaintext := []byte("hello tenant")

	ct, keyID, err := env.Encrypt(tenant, plaintext)
	require.NoError(t, err)
	assert.Equal(t, "v1", keyID)
	assert.Greater(t, len(ct), nonceLen)

	got, err := env.Decrypt(tenant, ct, keyID)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)
}

func TestEnvelope_CrossTenantFails(t *testing.T) {
	env := newTestEnvelope(t, map[string][]byte{"v1": makeKey(0x01)}, "v1")
	tenantA := uuid.New()
	tenantB := uuid.New()

	ct, keyID, err := env.Encrypt(tenantA, []byte("payload"))
	require.NoError(t, err)

	_, err = env.Decrypt(tenantB, ct, keyID)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrMasterKeyUnavailable)
	assert.NotErrorIs(t, err, ErrInvalidCiphertext)
}

func TestEnvelope_TamperedCiphertextFails(t *testing.T) {
	env := newTestEnvelope(t, map[string][]byte{"v1": makeKey(0x02)}, "v1")
	tenant := uuid.New()

	ct, keyID, err := env.Encrypt(tenant, []byte("important"))
	require.NoError(t, err)

	tampered := make([]byte, len(ct))
	copy(tampered, ct)
	tampered[len(tampered)-1] ^= 0x01

	_, err = env.Decrypt(tenant, tampered, keyID)
	require.Error(t, err)
}

func TestEnvelope_EmptyPlaintextRejected(t *testing.T) {
	env := newTestEnvelope(t, map[string][]byte{"v1": makeKey(0x03)}, "v1")
	_, _, err := env.Encrypt(uuid.New(), nil)
	assert.ErrorIs(t, err, ErrEmptyPlaintext)

	_, _, err = env.Encrypt(uuid.New(), []byte{})
	assert.ErrorIs(t, err, ErrEmptyPlaintext)
}

func TestEnvelope_TooShortCiphertextRejected(t *testing.T) {
	env := newTestEnvelope(t, map[string][]byte{"v1": makeKey(0x04)}, "v1")
	_, err := env.Decrypt(uuid.New(), []byte("short"), "v1")
	assert.ErrorIs(t, err, ErrInvalidCiphertext)
}

func TestEnvelope_NewEnvelope_RejectsWrongLengthKey(t *testing.T) {
	bad := bytes.Repeat([]byte{0x05}, 31)
	_, err := NewEnvelope(&MasterKeys{Keys: map[string][]byte{"v1": bad}, Active: "v1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMasterKeys)
}

func TestEnvelope_NewEnvelope_RejectsMissingActive(t *testing.T) {
	_, err := NewEnvelope(&MasterKeys{Keys: map[string][]byte{"v1": makeKey(0x06)}, Active: "v2"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMasterKeys)

	_, err = NewEnvelope(&MasterKeys{Keys: map[string][]byte{"v1": makeKey(0x06)}, Active: ""})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMasterKeys)

	_, err = NewEnvelope(&MasterKeys{Keys: map[string][]byte{}, Active: "v1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMasterKeys)

	_, err = NewEnvelope(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMasterKeys)
}

func TestEnvelope_Rotation_OldKeyIDStillDecrypts(t *testing.T) {
	env := newTestEnvelope(t, map[string][]byte{"v1": makeKey(0x07)}, "v1")
	tenant := uuid.New()
	plaintext := []byte("old write")

	ct, keyID, err := env.Encrypt(tenant, plaintext)
	require.NoError(t, err)
	assert.Equal(t, "v1", keyID)

	require.NoError(t, env.SetMasterKeys(&MasterKeys{
		Keys:   map[string][]byte{"v1": makeKey(0x07), "v2": makeKey(0x08)},
		Active: "v2",
	}))
	assert.Equal(t, "v2", env.ActiveKeyID())

	got, err := env.Decrypt(tenant, ct, keyID)
	require.NoError(t, err)
	assert.Equal(t, plaintext, got)

	ct2, keyID2, err := env.Encrypt(tenant, []byte("new write"))
	require.NoError(t, err)
	assert.Equal(t, "v2", keyID2)
	got2, err := env.Decrypt(tenant, ct2, keyID2)
	require.NoError(t, err)
	assert.Equal(t, []byte("new write"), got2)
}

func TestEnvelope_Rotation_MissingOldKey_Errors(t *testing.T) {
	env := newTestEnvelope(t, map[string][]byte{"v1": makeKey(0x09)}, "v1")
	tenant := uuid.New()

	ct, keyID, err := env.Encrypt(tenant, []byte("orphan"))
	require.NoError(t, err)

	require.NoError(t, env.SetMasterKeys(&MasterKeys{
		Keys:   map[string][]byte{"v2": makeKey(0x0A)},
		Active: "v2",
	}))

	_, err = env.Decrypt(tenant, ct, keyID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMasterKeyUnavailable)
}

func TestEnvelope_SetMasterKeys_HotReload(t *testing.T) {
	env := newTestEnvelope(t, map[string][]byte{"v1": makeKey(0x0B)}, "v1")
	tenant := uuid.New()

	seed, _, err := env.Encrypt(tenant, []byte("seed"))
	require.NoError(t, err)

	var stop atomic.Bool
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_, _, err := env.Encrypt(tenant, []byte("payload"))
				if err != nil {
					t.Errorf("encrypt: %v", err)
					return
				}
			}
		}()
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_, err := env.Decrypt(tenant, seed, "v1")
				if err != nil && !errors.Is(err, ErrMasterKeyUnavailable) {
					t.Errorf("decrypt: %v", err)
					return
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Every swap target retains all three keys so that any keyID returned
		// from Encrypt is decryptable; only the active label rotates. This
		// matches the production rotation contract (decryption keys persist
		// while the active write-side flips).
		swaps := []*MasterKeys{
			{Keys: map[string][]byte{"v1": makeKey(0x0B), "v2": makeKey(0x0C), "v3": makeKey(0x0D)}, Active: "v2"},
			{Keys: map[string][]byte{"v1": makeKey(0x0B), "v2": makeKey(0x0C), "v3": makeKey(0x0D)}, Active: "v1"},
			{Keys: map[string][]byte{"v1": makeKey(0x0B), "v2": makeKey(0x0C), "v3": makeKey(0x0D)}, Active: "v3"},
		}
		for i := 0; i < 200 && !stop.Load(); i++ {
			if err := env.SetMasterKeys(swaps[i%len(swaps)]); err != nil {
				t.Errorf("set: %v", err)
				return
			}
		}
	}()

	// let goroutines spin briefly
	for i := 0; i < 1000; i++ {
		ct, keyID, err := env.Encrypt(tenant, []byte("main"))
		require.NoError(t, err)
		got, err := env.Decrypt(tenant, ct, keyID)
		require.NoError(t, err)
		assert.Equal(t, []byte("main"), got)
	}
	stop.Store(true)
	wg.Wait()

	require.NoError(t, env.SetMasterKeys(&MasterKeys{
		Keys:   map[string][]byte{"v1": makeKey(0x0B), "v9": makeKey(0xFF)},
		Active: "v9",
	}))
	_, keyID, err := env.Encrypt(tenant, []byte("after"))
	require.NoError(t, err)
	assert.Equal(t, "v9", keyID)
}

func TestLoadMasterKeysFromEnv_FromEnvVar(t *testing.T) {
	t.Setenv(envMasterKeyFile, "")
	payload := masterKeysJSON{
		Keys:   map[string]string{"v1": base64.StdEncoding.EncodeToString(makeKey(0x11))},
		Active: "v1",
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	t.Setenv(envMasterKey, string(b))

	mk, err := LoadMasterKeysFromEnv()
	require.NoError(t, err)
	assert.Equal(t, "v1", mk.Active)
	require.Contains(t, mk.Keys, "v1")
	assert.Equal(t, makeKey(0x11), mk.Keys["v1"])

	env, err := NewEnvelope(mk)
	require.NoError(t, err)
	assert.Equal(t, "v1", env.ActiveKeyID())
}

func TestLoadMasterKeysFromEnv_FromFile(t *testing.T) {
	t.Setenv(envMasterKey, "")
	payload := masterKeysJSON{
		Keys: map[string]string{
			"v1": base64.StdEncoding.EncodeToString(makeKey(0x21)),
			"v2": base64.StdEncoding.EncodeToString(makeKey(0x22)),
		},
		Active: "v2",
	}
	b, err := json.Marshal(payload)
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "master.json")
	require.NoError(t, os.WriteFile(path, b, 0o600))
	t.Setenv(envMasterKeyFile, path)

	mk, err := LoadMasterKeysFromEnv()
	require.NoError(t, err)
	assert.Equal(t, "v2", mk.Active)
	assert.Len(t, mk.Keys, 2)
	assert.Equal(t, makeKey(0x21), mk.Keys["v1"])
	assert.Equal(t, makeKey(0x22), mk.Keys["v2"])
}

func TestLoadMasterKeysFromEnv_MissingFails(t *testing.T) {
	t.Setenv(envMasterKey, "")
	t.Setenv(envMasterKeyFile, "")
	_, err := LoadMasterKeysFromEnv()
	assert.ErrorIs(t, err, ErrMissingMasterKey)
}

func TestLoadMasterKeysFromEnv_BadBase64(t *testing.T) {
	t.Setenv(envMasterKeyFile, "")
	t.Setenv(envMasterKey, `{"keys":{"v1":"!!!not-base64!!!"},"active":"v1"}`)
	_, err := LoadMasterKeysFromEnv()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidMasterKeys)
}
