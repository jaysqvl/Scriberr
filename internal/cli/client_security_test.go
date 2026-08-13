package cli

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUploadSessionCacheRepairsPrivatePermissions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cache := map[string]cachedUploadSession{
		"fingerprint": {ID: "session", Token: "secret", ChunkSize: 1024, Fingerprint: "fingerprint"},
	}
	require.NoError(t, writeUploadSessionCache(cache))

	path, err := cliUploadCachePath()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0644))

	loaded, err := readUploadSessionCache()
	require.NoError(t, err)
	require.Equal(t, "secret", loaded["fingerprint"].Token)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
