package relay

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"moses-chat-bot/backend/internal/service/crypto"
	"moses-chat-bot/backend/internal/service/linker"
)

// cryptoEnvelope is an alias so the tests don't import the package name
// twice (we already alias to keep names readable in inbound_test.go).
type cryptoEnvelope = crypto.Envelope

// newCryptoEnvelope mints a fresh in-memory envelope with a random
// master key. No external state — safe to call concurrently across tests.
func newCryptoEnvelope(t *testing.T) *crypto.Envelope {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	mkJSON := map[string]interface{}{
		"keys":   map[string]string{"v1": base64.StdEncoding.EncodeToString(raw)},
		"active": "v1",
	}
	b, err := json.Marshal(mkJSON)
	require.NoError(t, err)
	t.Setenv("CHAT_BOT_MASTER_KEY", string(b))
	mk, err := crypto.LoadMasterKeysFromEnv()
	require.NoError(t, err)
	env, err := crypto.NewEnvelope(mk)
	require.NoError(t, err)
	return env
}

// newOfflineLinker constructs a Linker with nil store + nil mosesclient.
// Only the in-memory tables (RegisterKnown / IsKnown / lockout) are
// exercised; methods that need the DB (CreateCode, CompleteLink, Unlink)
// will panic if invoked from a relay path that uses them. The /unlink
// command in HandleInbound DOES call Unlink, so tests that exercise it
// must wire a real *db.Store fixture instead.
func newOfflineLinker(t *testing.T, env *crypto.Envelope) *linker.Linker {
	t.Helper()
	return linker.New(nil, env, nil)
}
