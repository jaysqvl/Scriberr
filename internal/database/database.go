package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"scriberr/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// DB is the global database instance
var DB *gorm.DB

// refreshTokenFamilyMigration gives existing refresh-token rows a temporary
// default while SQLite adds the new NOT NULL column. AutoMigrate removes the
// default after the rows have been backfilled.
type refreshTokenFamilyMigration struct {
	FamilyID string `gorm:"not null;default:'';type:varchar(36)"`
}

func (refreshTokenFamilyMigration) TableName() string {
	return "refresh_tokens"
}

// Initialize initializes the database connection with optimized settings
func Initialize(dbPath string) error {
	var err error

	// Create database directory if it doesn't exist
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %v", err)
	}

	// SQLite connection string with performance optimizations
	dsn := fmt.Sprintf("%s?"+
		"_pragma=foreign_keys(1)&"+ // Enable foreign keys
		"_pragma=journal_mode(WAL)&"+ // Use WAL mode for better concurrency
		"_pragma=synchronous(NORMAL)&"+ // Balance between safety and performance
		"_pragma=cache_size(-64000)&"+ // 64MB cache size
		"_pragma=temp_store(MEMORY)&"+ // Store temp tables in memory
		"_pragma=mmap_size(268435456)&"+ // 256MB mmap size
		"_timeout=30000", // 30 second timeout
		dbPath)

	// Open database connection with optimized config
	databaseLogger := gormlogger.New(log.New(os.Stdout, "", log.LstdFlags), gormlogger.Config{
		SlowThreshold:             time.Second,
		LogLevel:                  gormlogger.Warn,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      true,
		Colorful:                  false,
	})
	DB, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:          databaseLogger,
		CreateBatchSize: 100, // Optimize batch inserts
	})
	if err != nil {
		return fmt.Errorf("failed to connect to database: %v", err)
	}

	// Get underlying sql.DB for connection pool configuration
	sqlDB, err := DB.DB()
	if err != nil {
		return fmt.Errorf("failed to get underlying sql.DB: %v", err)
	}

	// Configure connection pool for optimal performance
	sqlDB.SetMaxOpenConns(10)                  // SQLite generally works well with lower connection counts
	sqlDB.SetMaxIdleConns(5)                   // Keep some connections idle
	sqlDB.SetConnMaxLifetime(30 * time.Minute) // Reset connections every 30 minutes
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)  // Close idle connections after 5 minutes

	if err := migrateRefreshTokenFamilies(DB); err != nil {
		return fmt.Errorf("failed to migrate refresh-token families: %v", err)
	}

	// Auto migrate the schema
	if err := DB.AutoMigrate(
		&models.TranscriptionJob{},
		&models.TranscriptionJobExecution{},
		&models.SpeakerMapping{},
		&models.MultiTrackFile{},
		&models.User{},
		&models.APIKey{},
		&models.TranscriptionProfile{},
		&models.LLMConfig{},
		&models.ChatSession{},
		&models.ChatMessage{},
		&models.SummaryTemplate{},
		&models.SummarySetting{},
		&models.Summary{},
		&models.Note{},
		&models.RefreshToken{},
		&models.RevokedAccessToken{},
		&models.UploadSession{},
		&models.UploadSessionFile{},
	); err != nil {
		return fmt.Errorf("failed to auto migrate: %v", err)
	}

	// Cleanup duplicate speaker mappings before creating unique index (for backward compatibility)
	// Keep the latest mapping for each (job_id, original_speaker) pair
	cleanupQuery := `
		DELETE FROM speaker_mappings 
		WHERE id NOT IN (
			SELECT MAX(id) 
			FROM speaker_mappings 
			GROUP BY transcription_job_id, original_speaker
		)
	`
	if err := DB.Exec(cleanupQuery).Error; err != nil {
		// Log warning but continue, as table might not exist yet or query might fail for other reasons
		// We don't want to block startup if this fails, but index creation might fail next.
		fmt.Printf("Warning: Failed to cleanup duplicate speaker mappings: %v\n", err)
	}

	// Add unique constraint for speaker mappings (transcription_job_id + original_speaker)
	if err := DB.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_speaker_mappings_unique ON speaker_mappings(transcription_job_id, original_speaker)").Error; err != nil {
		return fmt.Errorf("failed to create unique constraint for speaker mappings: %v", err)
	}

	// Execution history is response metadata, not a credential store. Remove
	// secrets captured by older versions before those rows can be returned.
	if err := DB.Model(&models.TranscriptionJobExecution{}).
		Where("actual_hf_token IS NOT NULL OR actual_api_key IS NOT NULL").
		Updates(map[string]interface{}{
			"actual_hf_token": nil,
			"actual_api_key":  nil,
		}).Error; err != nil {
		return fmt.Errorf("failed to scrub execution credentials: %v", err)
	}
	if err := DB.Where("expires_at <= ?", time.Now()).Delete(&models.RevokedAccessToken{}).Error; err != nil {
		return fmt.Errorf("failed to clean expired access-token revocations: %v", err)
	}

	return nil
}

func migrateRefreshTokenFamilies(db *gorm.DB) error {
	migrator := db.Migrator()
	if !migrator.HasTable(&models.RefreshToken{}) {
		return nil
	}

	columnWasMissing := !migrator.HasColumn(&models.RefreshToken{}, "FamilyID")
	if columnWasMissing {
		if err := migrator.AddColumn(&refreshTokenFamilyMigration{}, "FamilyID"); err != nil {
			return fmt.Errorf("add family_id column: %w", err)
		}
	}

	return db.Transaction(func(tx *gorm.DB) error {
		var tokenIDs []uint
		if err := tx.Table("refresh_tokens").
			Where("family_id IS NULL OR family_id = ?", "").
			Pluck("id", &tokenIDs).Error; err != nil {
			return fmt.Errorf("find tokens without a family: %w", err)
		}

		for _, tokenID := range tokenIDs {
			if err := tx.Table("refresh_tokens").
				Where("id = ? AND (family_id IS NULL OR family_id = ?)", tokenID, "").
				Update("family_id", uuid.NewString()).Error; err != nil {
				return fmt.Errorf("backfill family for refresh token %d: %w", tokenID, err)
			}
		}

		return nil
	})
}

// Close closes the database connection gracefully
func Close() error {
	if DB == nil {
		return nil
	}
	sqlDB, err := DB.DB()
	if err != nil {
		return err
	}
	err = sqlDB.Close()
	DB = nil // Set to nil after closing
	return err
}

// HealthCheck performs a health check on the database connection
func HealthCheck() error {
	if DB == nil {
		return fmt.Errorf("database connection is nil")
	}

	sqlDB, err := DB.DB()
	if err != nil {
		return fmt.Errorf("failed to get underlying sql.DB: %v", err)
	}

	// Test the connection with a ping
	if err := sqlDB.Ping(); err != nil {
		return fmt.Errorf("database ping failed: %v", err)
	}

	return nil
}

// GetConnectionStats returns database connection pool statistics
func GetConnectionStats() sql.DBStats {
	if DB == nil {
		return sql.DBStats{}
	}

	sqlDB, err := DB.DB()
	if err != nil {
		return sql.DBStats{}
	}

	return sqlDB.Stats()
}
