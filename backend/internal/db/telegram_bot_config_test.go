package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBotConfigRoundTrip(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	tenant := uuid.New()
	connectedBy := uuid.New()
	botID := int64(424242)
	botUsername := "moses_acme_bot"

	cfg, err := store.UpsertBotConfig(ctx, tenant,
		[]byte("ciphertext-blob"), "v1", &botID, &botUsername, "wh-secret", connectedBy)
	require.NoError(t, err)
	require.Equal(t, tenant, cfg.TenantID)
	require.Equal(t, []byte("ciphertext-blob"), cfg.EncryptedToken)
	require.Equal(t, "v1", cfg.EncryptionKeyID)
	require.NotNil(t, cfg.BotID)
	require.Equal(t, botID, *cfg.BotID)
	require.NotNil(t, cfg.BotUsername)
	require.Equal(t, botUsername, *cfg.BotUsername)
	require.Equal(t, "wh-secret", cfg.WebhookSecret)
	require.Equal(t, connectedBy, cfg.ConnectedByUserID)

	got, err := store.GetBotConfig(ctx, tenant)
	require.NoError(t, err)
	require.Equal(t, cfg.WebhookSecret, got.WebhookSecret)
	require.Equal(t, cfg.EncryptedToken, got.EncryptedToken)
}

func TestBotConfigUpsertReplaces(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	tenant := uuid.New()
	user := uuid.New()
	id1 := int64(1)
	name1 := "first_bot"

	first, err := store.UpsertBotConfig(ctx, tenant, []byte("c1"), "v1", &id1, &name1, "secret1", user)
	require.NoError(t, err)

	id2 := int64(2)
	name2 := "second_bot"
	second, err := store.UpsertBotConfig(ctx, tenant, []byte("c2"), "v1", &id2, &name2, "secret2", user)
	require.NoError(t, err)

	require.Equal(t, []byte("c2"), second.EncryptedToken)
	require.Equal(t, "second_bot", *second.BotUsername)
	require.Equal(t, "secret2", second.WebhookSecret)
	// connected_at is preserved across re-connects; updated_at advances.
	require.Equal(t, first.ConnectedAt, second.ConnectedAt)
	require.False(t, second.UpdatedAt.Before(first.UpdatedAt))

	// Exactly one row per tenant.
	all, err := store.ListBotConfigs(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
}

func TestBotConfigTenantIsolation(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	tenantA := uuid.New()
	tenantB := uuid.New()
	user := uuid.New()
	id := int64(9)
	name := "a_bot"

	_, err := store.UpsertBotConfig(ctx, tenantA, []byte("ca"), "v1", &id, &name, "sa", user)
	require.NoError(t, err)

	// Tenant B sees nothing.
	_, err = store.GetBotConfig(ctx, tenantB)
	require.True(t, IsNoRows(err), "tenant B must not see tenant A's bot config")

	// uuid.Nil is rejected.
	_, err = store.GetBotConfig(ctx, uuid.Nil)
	require.ErrorIs(t, err, ErrMissingTenantContext)
	_, err = store.UpsertBotConfig(ctx, uuid.Nil, []byte("x"), "v1", nil, nil, "s", user)
	require.ErrorIs(t, err, ErrMissingTenantContext)
	err = store.DeleteBotConfig(ctx, uuid.Nil)
	require.ErrorIs(t, err, ErrMissingTenantContext)
}

func TestBotConfigDelete(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	tenant := uuid.New()
	user := uuid.New()
	id := int64(3)
	name := "del_bot"

	_, err := store.UpsertBotConfig(ctx, tenant, []byte("c"), "v1", &id, &name, "s", user)
	require.NoError(t, err)

	require.NoError(t, store.DeleteBotConfig(ctx, tenant))
	_, err = store.GetBotConfig(ctx, tenant)
	require.True(t, IsNoRows(err))

	// Delete is a no-op (no error) when nothing is configured.
	require.NoError(t, store.DeleteBotConfig(ctx, tenant))
}
