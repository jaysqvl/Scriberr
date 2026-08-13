package models_test

import (
	"encoding/json"
	"testing"

	"scriberr/internal/models"
	"scriberr/internal/transcription"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranscriptionCredentialsAreWriteOnlyInJSON(t *testing.T) {
	hfToken := "hf_super_secret_value"
	apiKey := "sk-super-secret-value"
	params := models.WhisperXParams{ModelFamily: "whisperx", HfToken: &hfToken, APIKey: &apiKey}

	values := map[string]any{
		"job":       models.TranscriptionJob{Parameters: params},
		"profile":   models.TranscriptionProfile{Parameters: params},
		"execution": models.TranscriptionJobExecution{ActualParameters: params},
		"quick_job": transcription.QuickTranscriptionJob{Parameters: params},
	}

	for name, value := range values {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			body := string(encoded)
			assert.NotContains(t, body, hfToken)
			assert.NotContains(t, body, apiKey)
			assert.NotContains(t, body, `"hf_token":`)
			assert.NotContains(t, body, `"api_key":`)
			assert.Contains(t, body, `"model_family":"whisperx"`)
			assert.Contains(t, body, `"has_hf_token":true`)
			assert.Contains(t, body, `"has_api_key":true`)
		})
	}
}
