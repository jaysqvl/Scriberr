package repository

import (
	"context"
	"encoding/json"
	"testing"

	"scriberr/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newQueueRepositoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.TranscriptionJob{}, &models.TranscriptionQueueItem{}))
	return db
}

func createQueueRepositoryTestJob(t *testing.T, db *gorm.DB, id string, status models.JobStatus) {
	t.Helper()
	job := &models.TranscriptionJob{ID: id, AudioPath: id + ".wav", Status: status}
	require.NoError(t, db.Create(job).Error)
}

func TestTranscriptionQueueRepositoryPromotesAndClaimsExactItem(t *testing.T) {
	db := newQueueRepositoryTestDB(t)
	createQueueRepositoryTestJob(t, db, "audio-1", models.StatusCompleted)
	repo := NewTranscriptionQueueRepository(db)
	ctx := context.Background()
	apiKey := "private-openai-key"
	hfToken := "private-hf-token"
	items := []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{
			ModelFamily: "openai",
			Model:       "whisper-1",
			APIKey:      &apiKey,
			Verbose:     false,
			Fp16:        false,
			VadOnset:    0,
			BestOf:      0,
		}},
		{Parameters: models.WhisperXParams{ModelFamily: "whisper", Model: "large-v3", HfToken: &hfToken}},
	}
	require.NoError(t, repo.Append(ctx, "audio-1", items))

	waiting, err := repo.List(ctx, "audio-1", false)
	require.NoError(t, err)
	require.Len(t, waiting, 2)
	require.Equal(t, 1, waiting[0].Position)
	require.Equal(t, 2, waiting[1].Position)
	require.False(t, waiting[0].Parameters.Verbose)
	require.False(t, waiting[0].Parameters.Fp16)
	require.Zero(t, waiting[0].Parameters.VadOnset)
	require.Zero(t, waiting[0].Parameters.BestOf)
	require.Contains(t, waiting[0].ParametersJSON, `"verbose":false`)

	promoted, err := repo.PromoteNext(ctx, "audio-1")
	require.NoError(t, err)
	require.Equal(t, waiting[0].ID, promoted.ID)
	require.Equal(t, models.QueueStatusPending, promoted.Status)

	_, claimed, err := repo.ClaimPending(ctx, "audio-1", waiting[1].ID)
	require.NoError(t, err)
	require.False(t, claimed, "a stale/future item identity must never claim the parent job")

	claimedItem, claimed, err := repo.ClaimPending(ctx, "audio-1", promoted.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, models.QueueStatusProcessing, claimedItem.Status)

	var job models.TranscriptionJob
	require.NoError(t, db.First(&job, "id = ?", "audio-1").Error)
	require.Equal(t, models.StatusProcessing, job.Status)
	require.Equal(t, "whisper-1", job.Parameters.Model)
	require.NotNil(t, job.Parameters.APIKey)
	require.Equal(t, apiKey, *job.Parameters.APIKey)

	require.NoError(t, repo.Finalize(ctx, "audio-1", promoted.ID, models.QueueStatusCompleted, "", nil))
	require.NoError(t, db.First(&job, "id = ?", "audio-1").Error)
	require.Equal(t, models.StatusCompleted, job.Status)
	require.Nil(t, job.ErrorMessage)
	finished, err := repo.FindByID(ctx, "audio-1", promoted.ID)
	require.NoError(t, err)
	require.Equal(t, models.QueueStatusCompleted, finished.Status)
	require.Nil(t, finished.Parameters.APIKey, "terminal queue records must not retain provider credentials")
	require.Nil(t, finished.Parameters.HfToken)

	encoded, err := json.Marshal(finished)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), apiKey)
}

func TestTranscriptionQueueRepositoryReordersAndCancelsOnlyWaitingItems(t *testing.T) {
	db := newQueueRepositoryTestDB(t)
	createQueueRepositoryTestJob(t, db, "audio-2", models.StatusProcessing)
	repo := NewTranscriptionQueueRepository(db)
	ctx := context.Background()
	items := []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{Model: "tiny"}},
		{Parameters: models.WhisperXParams{Model: "small"}},
		{Parameters: models.WhisperXParams{Model: "medium"}},
	}
	require.NoError(t, repo.Append(ctx, "audio-2", items))
	waiting, err := repo.List(ctx, "audio-2", false)
	require.NoError(t, err)
	require.Len(t, waiting, 3)

	require.NoError(t, repo.ReorderQueued(ctx, "audio-2", []string{waiting[2].ID, waiting[0].ID, waiting[1].ID}))
	reordered, err := repo.List(ctx, "audio-2", false)
	require.NoError(t, err)
	require.Equal(t, []string{"medium", "tiny", "small"}, []string{
		reordered[0].Parameters.Model,
		reordered[1].Parameters.Model,
		reordered[2].Parameters.Model,
	})

	require.NoError(t, repo.CancelQueued(ctx, "audio-2", reordered[1].ID))
	remaining, err := repo.List(ctx, "audio-2", false)
	require.NoError(t, err)
	require.Len(t, remaining, 2)
	require.Equal(t, 1, remaining[0].Position)
	require.Equal(t, 2, remaining[1].Position)
	require.ErrorIs(t, repo.CancelQueued(ctx, "audio-2", reordered[1].ID), ErrQueueItemNotQueued)
	require.ErrorIs(t, repo.ReorderQueued(ctx, "audio-2", []string{remaining[0].ID}), ErrQueueOrderMismatch)

	cleared, err := repo.ClearQueued(ctx, "audio-2")
	require.NoError(t, err)
	require.Equal(t, int64(2), cleared)
	remaining, err = repo.List(ctx, "audio-2", false)
	require.NoError(t, err)
	require.Empty(t, remaining)
}
