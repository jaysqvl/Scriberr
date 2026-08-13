package database

import (
	"path/filepath"
	"testing"

	"scriberr/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitializeScrubsHistoricalExecutionCredentials(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "scriberr.db")
	require.NoError(t, Initialize(databasePath))
	t.Cleanup(func() { _ = Close() })

	job := models.TranscriptionJob{AudioPath: "/tmp/audio.wav", Status: models.StatusCompleted}
	require.NoError(t, DB.Create(&job).Error)
	hfToken := "hf_historical_secret"
	apiKey := "sk_historical_secret"
	execution := models.TranscriptionJobExecution{
		TranscriptionJobID: job.ID,
		Status:             models.StatusCompleted,
		ActualParameters: models.WhisperXParams{
			HfToken: &hfToken,
			APIKey:  &apiKey,
		},
	}
	require.NoError(t, DB.Create(&execution).Error)
	require.NoError(t, Close())
	require.NoError(t, Initialize(databasePath))

	var stored models.TranscriptionJobExecution
	require.NoError(t, DB.Where("id = ?", execution.ID).First(&stored).Error)
	assert.Nil(t, stored.ActualParameters.HfToken)
	assert.Nil(t, stored.ActualParameters.APIKey)
}
