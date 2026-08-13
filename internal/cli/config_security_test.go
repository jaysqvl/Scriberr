package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveConfigUsesOwnerOnlyPermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	viper.Reset()
	t.Cleanup(viper.Reset)

	path, err := SaveConfig("https://scriberr.example", "cli-secret", "")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".scriberr.yaml"), path)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	require.NoError(t, os.Chmod(path, 0644))
	_, err = SaveConfig("https://scriberr.example", "replacement-secret", "")
	require.NoError(t, err)
	info, err = os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
