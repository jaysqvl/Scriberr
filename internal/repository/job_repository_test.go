package repository

import (
	"context"
	"testing"
	"time"

	"scriberr/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestJobRepositoryListWithParamsSortAllowlist(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.TranscriptionJob{}))

	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	createJob := func(id, title string, createdAt time.Time) {
		job := models.TranscriptionJob{
			ID:        id,
			Title:     &title,
			Status:    models.StatusCompleted,
			AudioPath: id + ".wav",
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		}
		require.NoError(t, db.Create(&job).Error)
	}

	createJob("old", "alpha", base)
	createJob("new", "beta", base.Add(time.Minute))

	repo := NewJobRepository(db)

	t.Run("allows known columns", func(t *testing.T) {
		jobs, count, err := repo.ListWithParams(context.Background(), 0, 10, "title", "asc", "", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), count)
		require.Equal(t, []string{"old", "new"}, jobIDs(jobs))
	})

	t.Run("defaults malicious column and direction", func(t *testing.T) {
		jobs, count, err := repo.ListWithParams(
			context.Background(),
			0,
			10,
			"CASE WHEN (SELECT count(*) FROM sqlite_master) > 0 THEN title ELSE id END",
			"asc; drop table transcription_jobs; --",
			"",
			nil,
		)
		require.NoError(t, err)
		require.Equal(t, int64(2), count)
		require.Equal(t, []string{"new", "old"}, jobIDs(jobs))
	})
}

func jobIDs(jobs []models.TranscriptionJob) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	return ids
}
