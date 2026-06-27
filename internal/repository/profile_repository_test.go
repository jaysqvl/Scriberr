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

func TestProfileRepositoryListOrdersByName(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.TranscriptionProfile{}))

	createdAt := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	profiles := []models.TranscriptionProfile{
		{ID: "zeta", Name: "zeta", CreatedAt: createdAt.Add(4 * time.Minute)},
		{ID: "beta", Name: "beta", CreatedAt: createdAt.Add(3 * time.Minute)},
		{ID: "alpha-later", Name: "Alpha", CreatedAt: createdAt.Add(2 * time.Minute)},
		{ID: "alpha-earlier", Name: "alpha", CreatedAt: createdAt.Add(time.Minute)},
	}

	for i := range profiles {
		require.NoError(t, db.Create(&profiles[i]).Error)
	}

	repo := NewProfileRepository(db)
	got, count, err := repo.List(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Equal(t, int64(4), count)
	require.Equal(t, []string{"alpha", "Alpha", "beta", "zeta"}, profileNames(got))

	gotPage, count, err := repo.List(context.Background(), 1, 2)
	require.NoError(t, err)
	require.Equal(t, int64(4), count)
	require.Equal(t, []string{"Alpha", "beta"}, profileNames(gotPage))
}

func profileNames(profiles []models.TranscriptionProfile) []string {
	names := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		names = append(names, profile.Name)
	}
	return names
}
