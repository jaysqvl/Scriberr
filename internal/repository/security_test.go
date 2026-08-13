package repository

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"scriberr/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openSecurityTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "security.db") + "?_pragma=journal_mode(WAL)&_timeout=30000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.User{}, &models.RefreshToken{}))
	return db
}

func TestCreateInitialAdminIsAtomicUnderConcurrency(t *testing.T) {
	db := openSecurityTestDB(t)
	repo := NewUserRepository(db)

	const contenders = 16
	start := make(chan struct{})
	errorsByAttempt := make(chan error, contenders)
	var wg sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			errorsByAttempt <- repo.CreateInitialAdmin(context.Background(), &models.User{
				Username: fmt.Sprintf("admin-%d", index),
				Password: "hashed-password",
			})
		}(index)
	}
	close(start)
	wg.Wait()
	close(errorsByAttempt)

	successes := 0
	for err := range errorsByAttempt {
		if err == nil {
			successes++
			continue
		}
		assert.ErrorIs(t, err, ErrInitialUserExists)
	}
	assert.Equal(t, 1, successes)

	var count int64
	require.NoError(t, db.Model(&models.User{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestRefreshRotationCreatesOneSuccessorAndRevokesReplayFamily(t *testing.T) {
	db := openSecurityTestDB(t)
	repo := NewRefreshTokenRepository(db)
	user := models.User{Username: "admin", Password: "hashed-password"}
	require.NoError(t, db.Create(&user).Error)

	familyID := uuid.NewString()
	current := models.RefreshToken{
		UserID:    user.ID,
		FamilyID:  familyID,
		Hashed:    "current-hash",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, repo.Create(context.Background(), &current))

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			replacement := models.RefreshToken{
				Hashed:    fmt.Sprintf("replacement-%d", index),
				ExpiresAt: time.Now().Add(time.Hour),
			}
			_, err := repo.Rotate(context.Background(), current.Hashed, &replacement, time.Now())
			results <- err
		}(index)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	replays := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRefreshTokenReplay):
			replays++
		default:
			t.Fatalf("unexpected refresh rotation error: %v", err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, replays)

	var family []models.RefreshToken
	require.NoError(t, db.Where("family_id = ?", familyID).Order("id ASC").Find(&family).Error)
	require.Len(t, family, 2)
	for _, token := range family {
		assert.True(t, token.Revoked)
	}
}
