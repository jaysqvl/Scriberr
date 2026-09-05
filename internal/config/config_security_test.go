package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJWTSecretFileIsPersistentAndPrivate(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "jwt_secret")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("JWT_SECRET_FILE", secretFile)

	first := getJWTSecret()
	second := getJWTSecret()
	require.Len(t, first, 64)
	require.Equal(t, first, second)

	info, err := os.Stat(secretFile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestJWTSecretRegeneratesEmptyFileAndRepairsPermissions(t *testing.T) {
	secretFile := filepath.Join(t.TempDir(), "jwt_secret")
	require.NoError(t, os.WriteFile(secretFile, []byte("  \n"), 0644))
	t.Setenv("JWT_SECRET", "")
	t.Setenv("JWT_SECRET_FILE", secretFile)

	secret := getJWTSecret()
	require.Len(t, secret, 64)

	info, err := os.Stat(secretFile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestSecureCookiesModeDefaultsToAutoAndHonorsOverrides(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("SECURE_COOKIES", "")
	require.Equal(t, "auto", Load().SecureCookiesMode)

	t.Setenv("SECURE_COOKIES", "true")
	require.Equal(t, "true", Load().SecureCookiesMode)

	t.Setenv("SECURE_COOKIES", "false")
	require.Equal(t, "false", Load().SecureCookiesMode)
}
