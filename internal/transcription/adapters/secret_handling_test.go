package adapters

import (
	"strings"
	"testing"

	"scriberr/internal/transcription/interfaces"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderTokensNeverEnterProcessArguments(t *testing.T) {
	const token = "hf_process_secret"
	input := interfaces.AudioInput{FilePath: "/tmp/audio.wav"}

	whisper := NewWhisperXAdapter("/tmp/whisper")
	whisperArgs, err := whisper.buildWhisperXArgs(input, map[string]interface{}{
		"hf_token": token,
		"model":    "small",
	}, "/tmp/output")
	require.NoError(t, err)
	assert.NotContains(t, strings.Join(whisperArgs, " "), token)
	assert.NotContains(t, whisperArgs, "--hf_token")

	pyannote := NewPyAnnoteAdapter("/tmp/pyannote")
	pyannoteArgs, err := pyannote.buildPyAnnoteArgs(input, map[string]interface{}{
		"hf_token":      token,
		"output_format": "json",
	}, "/tmp/output")
	require.NoError(t, err)
	assert.NotContains(t, strings.Join(pyannoteArgs, " "), token)
	assert.NotContains(t, pyannoteArgs, "--hf-token")

	environment := withEnvironmentValue([]string{"HF_TOKEN=old", "PATH=/bin"}, "HF_TOKEN", token)
	assert.Contains(t, environment, "HF_TOKEN="+token)
	assert.NotContains(t, environment, "HF_TOKEN=old")
}
