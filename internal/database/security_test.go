package database

import (
	"path/filepath"
	"testing"
	"time"

	"scriberr/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyRefreshToken struct {
	ID        uint      `gorm:"primaryKey"`
	UserID    uint      `gorm:"not null;index"`
	Hashed    string    `gorm:"not null;uniqueIndex;type:varchar(128)"`
	ExpiresAt time.Time `gorm:"not null;index"`
	Revoked   bool      `gorm:"not null;default:false;index"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (legacyRefreshToken) TableName() string {
	return "refresh_tokens"
}

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

func TestInitializeMigratesLegacyRefreshTokenFamilies(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "scriberr.db")
	legacyDB, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, legacyDB.AutoMigrate(&legacyRefreshToken{}))

	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	legacyTokens := []legacyRefreshToken{
		{UserID: 1, Hashed: "legacy-token-one", ExpiresAt: expiresAt},
		{UserID: 1, Hashed: "legacy-token-two", ExpiresAt: expiresAt},
	}
	require.NoError(t, legacyDB.Create(&legacyTokens).Error)
	legacySQLDB, err := legacyDB.DB()
	require.NoError(t, err)
	require.NoError(t, legacySQLDB.Close())

	require.NoError(t, Initialize(databasePath))
	t.Cleanup(func() { _ = Close() })

	var migratedTokens []models.RefreshToken
	require.NoError(t, DB.Order("id ASC").Find(&migratedTokens).Error)
	require.Len(t, migratedTokens, 2)
	assert.Equal(t, "legacy-token-one", migratedTokens[0].Hashed)
	assert.Equal(t, "legacy-token-two", migratedTokens[1].Hashed)
	assert.NotEmpty(t, migratedTokens[0].FamilyID)
	assert.NotEmpty(t, migratedTokens[1].FamilyID)
	assert.NotEqual(t, migratedTokens[0].FamilyID, migratedTokens[1].FamilyID)
	require.NoError(t, uuid.Validate(migratedTokens[0].FamilyID))
	require.NoError(t, uuid.Validate(migratedTokens[1].FamilyID))

	columnTypes, err := DB.Migrator().ColumnTypes(&models.RefreshToken{})
	require.NoError(t, err)
	foundFamilyID := false
	for _, columnType := range columnTypes {
		if columnType.Name() != "family_id" {
			continue
		}
		foundFamilyID = true
		nullable, ok := columnType.Nullable()
		require.True(t, ok)
		assert.False(t, nullable)
		_, hasDefault := columnType.DefaultValue()
		assert.False(t, hasDefault)
		break
	}
	require.True(t, foundFamilyID, "family_id column was not created")

	firstFamilyID := migratedTokens[0].FamilyID
	secondFamilyID := migratedTokens[1].FamilyID
	require.NoError(t, Close())
	require.NoError(t, Initialize(databasePath))
	require.NoError(t, DB.Order("id ASC").Find(&migratedTokens).Error)
	require.Len(t, migratedTokens, 2)
	assert.Equal(t, firstFamilyID, migratedTokens[0].FamilyID)
	assert.Equal(t, secondFamilyID, migratedTokens[1].FamilyID)
}
