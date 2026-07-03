package cmdutil_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/christianselig/apollo-backend/internal/cmdutil"
)

// writeTestP8 writes a valid PKCS8 EC P-256 key (the same shape as an Apple
// .p8 auth key) and returns its path.
func writeTestP8(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "apple.p8")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600))
	return path
}

// clearAppleEnv isolates each case from the ambient environment; t.Setenv
// also marks the test as non-parallel, which env-dependent tests must be.
func clearAppleEnv(t *testing.T) {
	t.Helper()
	for _, v := range []string{"APPLE_KEY_PATH", "APPLE_KEY_ID", "APPLE_TEAM_ID", "APPLE_APNS_TOPIC"} {
		t.Setenv(v, "")
	}
}

func TestLoadAPNS_AllUnsetMeansBarkOnly(t *testing.T) {
	t.Setenv("APPLE_KEY_PATH", "")
	t.Setenv("APPLE_KEY_ID", "")
	t.Setenv("APPLE_TEAM_ID", "")
	t.Setenv("APPLE_APNS_TOPIC", "")

	tok, topic, err := cmdutil.LoadAPNS()
	require.NoError(t, err)
	assert.Nil(t, tok)
	assert.Empty(t, topic)
}

func TestLoadAPNS_AllSetLoadsToken(t *testing.T) {
	clearAppleEnv(t)
	t.Setenv("APPLE_KEY_PATH", writeTestP8(t))
	t.Setenv("APPLE_KEY_ID", "ABC123XYZ9")
	t.Setenv("APPLE_TEAM_ID", "A1B2C3D4E5")
	t.Setenv("APPLE_APNS_TOPIC", "com.example.Apollo")

	tok, topic, err := cmdutil.LoadAPNS()
	require.NoError(t, err)
	require.NotNil(t, tok)
	assert.NotNil(t, tok.AuthKey)
	assert.Equal(t, "ABC123XYZ9", tok.KeyID)
	assert.Equal(t, "A1B2C3D4E5", tok.TeamID)
	assert.Equal(t, "com.example.Apollo", topic)
}

func TestLoadAPNS_PartialConfigNamesMissingVars(t *testing.T) {
	clearAppleEnv(t)
	t.Setenv("APPLE_KEY_ID", "ABC123XYZ9")

	tok, _, err := cmdutil.LoadAPNS()
	require.Error(t, err)
	assert.Nil(t, tok)
	assert.Contains(t, err.Error(), "APPLE_KEY_PATH")
	assert.Contains(t, err.Error(), "APPLE_TEAM_ID")
	assert.Contains(t, err.Error(), "APPLE_APNS_TOPIC")
	assert.Contains(t, err.Error(), "Bark-only")
}

func TestLoadAPNS_UnreadableKeyFileErrors(t *testing.T) {
	clearAppleEnv(t)
	t.Setenv("APPLE_KEY_PATH", filepath.Join(t.TempDir(), "missing.p8"))
	t.Setenv("APPLE_KEY_ID", "ABC123XYZ9")
	t.Setenv("APPLE_TEAM_ID", "A1B2C3D4E5")
	t.Setenv("APPLE_APNS_TOPIC", "com.example.Apollo")

	tok, _, err := cmdutil.LoadAPNS()
	require.Error(t, err)
	assert.Nil(t, tok)
	assert.Contains(t, err.Error(), "APPLE_KEY_PATH")
}
