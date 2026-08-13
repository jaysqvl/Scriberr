package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"scriberr/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrInitialUserExists   = errors.New("initial administrator already exists")
	ErrRefreshTokenInvalid = errors.New("refresh token is invalid")
	ErrRefreshTokenReplay  = errors.New("refresh token replay detected")
)

// UserRepository handles user-specific database operations
type UserRepository interface {
	Repository[models.User]
	FindByUsername(ctx context.Context, username string) (*models.User, error)
	Count(ctx context.Context) (int64, error)
	CountWithAutoTranscription(ctx context.Context) (int64, error)
	CreateInitialAdmin(ctx context.Context, user *models.User) error
	UpdatePasswordAndRevokeSessions(ctx context.Context, userID uint, hashedPassword string) error
}

type userRepository struct {
	*BaseRepository[models.User]
}

func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{
		BaseRepository: NewBaseRepository[models.User](db),
	}
}

func (r *userRepository) FindByUsername(ctx context.Context, username string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.User{}).Count(&count).Error
	return count, err
}

func (r *userRepository) CountWithAutoTranscription(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.User{}).Where("auto_transcription_enabled = ?", true).Count(&count).Error
	return count, err
}

func (r *userRepository) CreateInitialAdmin(ctx context.Context, user *models.User) error {
	for attempt := 0; attempt < 4; attempt++ {
		now := time.Now()
		result := r.db.WithContext(ctx).Exec(`
			INSERT INTO users (username, password, token_version, auto_transcription_enabled, created_at, updated_at)
			SELECT ?, ?, 0, ?, ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM users)
		`, user.Username, user.Password, user.AutoTranscriptionEnabled, now, now)
		if result.Error == nil {
			if result.RowsAffected != 1 {
				return ErrInitialUserExists
			}
			return r.db.WithContext(ctx).Where("username = ?", user.Username).First(user).Error
		}
		if !isTransientDatabaseLock(result.Error) {
			return result.Error
		}
		if err := waitForDatabaseRetry(ctx, attempt); err != nil {
			return err
		}
	}
	return fmt.Errorf("create initial administrator: database remained busy")
}

func (r *userRepository) UpdatePasswordAndRevokeSessions(ctx context.Context, userID uint, hashedPassword string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&models.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
			"password":      hashedPassword,
			"token_version": gorm.Expr("token_version + 1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		now := time.Now()
		return tx.Model(&models.RefreshToken{}).
			Where("user_id = ? AND revoked = ?", userID, false).
			Updates(map[string]interface{}{"revoked": true, "revoked_at": &now}).Error
	})
}

// JobRepository handles transcription job operations
type JobRepository interface {
	Repository[models.TranscriptionJob]
	FindWithAssociations(ctx context.Context, id string) (*models.TranscriptionJob, error)
	FindActiveTrackJobs(ctx context.Context, parentJobID string) ([]models.TranscriptionJob, error)
	FindLatestCompletedExecution(ctx context.Context, jobID string) (*models.TranscriptionJobExecution, error)
	ListWithParams(ctx context.Context, offset, limit int, sortBy, sortOrder, searchQuery string, updatedAfter *time.Time) ([]models.TranscriptionJob, int64, error)
	ListByUser(ctx context.Context, userID uint, offset, limit int) ([]models.TranscriptionJob, int64, error)
	UpdateTranscript(ctx context.Context, jobID string, transcript string) error
	CreateExecution(ctx context.Context, execution *models.TranscriptionJobExecution) error
	UpdateExecution(ctx context.Context, execution *models.TranscriptionJobExecution) error
	ListExecutionsByJobID(ctx context.Context, jobID string) ([]models.TranscriptionJobExecution, error)
	FindExecution(ctx context.Context, jobID, executionID string) (*models.TranscriptionJobExecution, error)
	FindLatestExecution(ctx context.Context, jobID string) (*models.TranscriptionJobExecution, error)
	SetPinnedExecution(ctx context.Context, jobID string, executionID *string) error
	DeleteExecutionsByJobID(ctx context.Context, jobID string) error
	DeleteMultiTrackFilesByJobID(ctx context.Context, jobID string) error
	UpdateStatus(ctx context.Context, jobID string, status models.JobStatus) error
	UpdateError(ctx context.Context, jobID string, errorMsg string) error
	FindByStatus(ctx context.Context, status models.JobStatus) ([]models.TranscriptionJob, error)
	CountByStatus(ctx context.Context, status models.JobStatus) (int64, error)
	UpdateSummary(ctx context.Context, jobID string, summary string) error
}

type jobRepository struct {
	*BaseRepository[models.TranscriptionJob]
}

func NewJobRepository(db *gorm.DB) JobRepository {
	return &jobRepository{
		BaseRepository: NewBaseRepository[models.TranscriptionJob](db),
	}
}

func (r *jobRepository) FindWithAssociations(ctx context.Context, id string) (*models.TranscriptionJob, error) {
	var job models.TranscriptionJob
	err := r.db.WithContext(ctx).
		Preload("MultiTrackFiles").
		Where("id = ?", id).
		First(&job).Error
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *jobRepository) ListWithParams(ctx context.Context, offset, limit int, sortBy, sortOrder, searchQuery string, updatedAfter *time.Time) ([]models.TranscriptionJob, int64, error) {
	var jobs []models.TranscriptionJob
	var count int64

	db := r.db.WithContext(ctx).Model(&models.TranscriptionJob{})

	// Handle delta sync if updatedAfter provided
	if updatedAfter != nil {
		db = db.Unscoped().Where("updated_at > ?", *updatedAfter)
	}

	// Apply search filter
	if searchQuery != "" {
		search := "%" + searchQuery + "%"
		db = db.Where("title LIKE ? OR audio_path LIKE ?", search, search)
	}

	// Count total matching records
	if err := db.Count(&count).Error; err != nil {
		return nil, 0, err
	}

	db = db.Order(normalizeJobListSort(sortBy, sortOrder))

	// Apply pagination
	err := db.Offset(offset).Limit(limit).Find(&jobs).Error
	if err != nil {
		return nil, 0, err
	}

	return jobs, count, nil
}

func normalizeJobListSort(sortBy, sortOrder string) string {
	columns := map[string]string{
		"":           "created_at",
		"id":         "id",
		"title":      "title",
		"status":     "status",
		"audio_path": "audio_path",
		"created_at": "created_at",
		"updated_at": "updated_at",
	}

	column, ok := columns[strings.ToLower(strings.TrimSpace(sortBy))]
	if !ok {
		column = columns[""]
	}

	direction := strings.ToLower(strings.TrimSpace(sortOrder))
	if direction != "asc" && direction != "desc" {
		direction = "desc"
	}

	return column + " " + direction
}

func (r *jobRepository) ListByUser(ctx context.Context, userID uint, offset, limit int) ([]models.TranscriptionJob, int64, error) {
	// Note: Currently TranscriptionJob doesn't have a UserID field in the provided model.
	// Assuming we might need to add it or this is a placeholder for future multi-user support.
	// For now, we'll just return all jobs as the current app seems single-user focused or
	// missing the link.
	// TODO: Add UserID to TranscriptionJob model if multi-user isolation is required.
	return r.List(ctx, offset, limit)
}

func (r *jobRepository) UpdateTranscript(ctx context.Context, jobID string, transcript string) error {
	return r.db.WithContext(ctx).Model(&models.TranscriptionJob{}).
		Where("id = ?", jobID).
		Update("transcript", transcript).Error
}

func (r *jobRepository) CreateExecution(ctx context.Context, execution *models.TranscriptionJobExecution) error {
	return r.db.WithContext(ctx).Create(execution).Error
}

func (r *jobRepository) UpdateExecution(ctx context.Context, execution *models.TranscriptionJobExecution) error {
	return r.db.WithContext(ctx).Save(execution).Error
}

func (r *jobRepository) ListExecutionsByJobID(ctx context.Context, jobID string) ([]models.TranscriptionJobExecution, error) {
	var executions []models.TranscriptionJobExecution
	err := r.db.WithContext(ctx).
		Where("transcription_job_id = ?", jobID).
		Order("started_at ASC, created_at ASC").
		Find(&executions).Error
	return executions, err
}

func (r *jobRepository) FindExecution(ctx context.Context, jobID, executionID string) (*models.TranscriptionJobExecution, error) {
	var execution models.TranscriptionJobExecution
	err := r.db.WithContext(ctx).
		Where("transcription_job_id = ? AND id = ?", jobID, executionID).
		First(&execution).Error
	if err != nil {
		return nil, err
	}
	return &execution, nil
}

func (r *jobRepository) FindLatestExecution(ctx context.Context, jobID string) (*models.TranscriptionJobExecution, error) {
	var execution models.TranscriptionJobExecution
	err := r.db.WithContext(ctx).
		Where("transcription_job_id = ?", jobID).
		Order("started_at DESC, created_at DESC").
		First(&execution).Error
	if err != nil {
		return nil, err
	}
	return &execution, nil
}

func (r *jobRepository) SetPinnedExecution(ctx context.Context, jobID string, executionID *string) error {
	return r.db.WithContext(ctx).Model(&models.TranscriptionJob{}).
		Where("id = ?", jobID).
		Update("pinned_execution_id", executionID).Error
}

func (r *jobRepository) DeleteExecutionsByJobID(ctx context.Context, jobID string) error {
	return r.db.WithContext(ctx).Where("transcription_job_id = ?", jobID).Delete(&models.TranscriptionJobExecution{}).Error
}

func (r *jobRepository) DeleteMultiTrackFilesByJobID(ctx context.Context, jobID string) error {
	return r.db.WithContext(ctx).Where("transcription_job_id = ?", jobID).Delete(&models.MultiTrackFile{}).Error
}

func (r *jobRepository) FindActiveTrackJobs(ctx context.Context, parentJobID string) ([]models.TranscriptionJob, error) {
	var jobs []models.TranscriptionJob
	err := r.db.WithContext(ctx).
		Where("id LIKE ? AND status IN (?)", "track_"+parentJobID+"_%", []string{"processing", "pending"}).
		Find(&jobs).Error
	return jobs, err
}

func (r *jobRepository) FindLatestCompletedExecution(ctx context.Context, jobID string) (*models.TranscriptionJobExecution, error) {
	var execution models.TranscriptionJobExecution
	err := r.db.WithContext(ctx).
		Where("transcription_job_id = ? AND status = ?", jobID, models.StatusCompleted).
		Order("completed_at DESC, created_at DESC").
		First(&execution).Error
	if err != nil {
		return nil, err
	}
	return &execution, nil
}

func (r *jobRepository) UpdateStatus(ctx context.Context, jobID string, status models.JobStatus) error {
	return r.db.WithContext(ctx).Model(&models.TranscriptionJob{}).Where("id = ?", jobID).Update("status", status).Error
}

func (r *jobRepository) UpdateError(ctx context.Context, jobID string, errorMsg string) error {
	return r.db.WithContext(ctx).Model(&models.TranscriptionJob{}).Where("id = ?", jobID).Update("error_message", errorMsg).Error
}

func (r *jobRepository) FindByStatus(ctx context.Context, status models.JobStatus) ([]models.TranscriptionJob, error) {
	var jobs []models.TranscriptionJob
	err := r.db.WithContext(ctx).Where("status = ?", status).Find(&jobs).Error
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *jobRepository) CountByStatus(ctx context.Context, status models.JobStatus) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.TranscriptionJob{}).Where("status = ?", status).Count(&count).Error
	return count, err
}

func (r *jobRepository) UpdateSummary(ctx context.Context, jobID string, summary string) error {
	return r.db.WithContext(ctx).Model(&models.TranscriptionJob{}).Where("id = ?", jobID).Update("summary", summary).Error
}

// APIKeyRepository handles API key operations
type APIKeyRepository interface {
	Repository[models.APIKey]
	FindByKey(ctx context.Context, key string) (*models.APIKey, error)
	ListActive(ctx context.Context) ([]models.APIKey, error)
	Revoke(ctx context.Context, id uint) error
}

type apiKeyRepository struct {
	*BaseRepository[models.APIKey]
}

func NewAPIKeyRepository(db *gorm.DB) APIKeyRepository {
	return &apiKeyRepository{
		BaseRepository: NewBaseRepository[models.APIKey](db),
	}
}

func (r *apiKeyRepository) FindByKey(ctx context.Context, key string) (*models.APIKey, error) {
	var apiKey models.APIKey
	err := r.db.WithContext(ctx).Where("key = ?", key).First(&apiKey).Error
	if err != nil {
		return nil, err
	}
	return &apiKey, nil
}

func (r *apiKeyRepository) ListActive(ctx context.Context) ([]models.APIKey, error) {
	var apiKeys []models.APIKey
	err := r.db.WithContext(ctx).Where("is_active = ?", true).Find(&apiKeys).Error
	if err != nil {
		return nil, err
	}
	return apiKeys, nil
}

func (r *apiKeyRepository) Revoke(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Model(&models.APIKey{}).Where("id = ?", id).Update("is_active", false).Error
}

// ProfileRepository handles transcription profile operations
type ProfileRepository interface {
	Repository[models.TranscriptionProfile]
	FindDefault(ctx context.Context) (*models.TranscriptionProfile, error)
	FindByName(ctx context.Context, name string) (*models.TranscriptionProfile, error)
}

type profileRepository struct {
	*BaseRepository[models.TranscriptionProfile]
}

func NewProfileRepository(db *gorm.DB) ProfileRepository {
	return &profileRepository{
		BaseRepository: NewBaseRepository[models.TranscriptionProfile](db),
	}
}

func (r *profileRepository) List(ctx context.Context, offset, limit int) ([]models.TranscriptionProfile, int64, error) {
	var profiles []models.TranscriptionProfile
	var count int64

	db := r.db.WithContext(ctx).Model(&models.TranscriptionProfile{})

	if err := db.Count(&count).Error; err != nil {
		return nil, 0, err
	}

	err := db.
		Order("LOWER(name) ASC").
		Order("created_at ASC").
		Order("id ASC").
		Offset(offset).
		Limit(limit).
		Find(&profiles).Error
	if err != nil {
		return nil, 0, err
	}

	return profiles, count, nil
}

func (r *profileRepository) FindDefault(ctx context.Context) (*models.TranscriptionProfile, error) {
	var profile models.TranscriptionProfile
	err := r.db.WithContext(ctx).Where("is_default = ?", true).First(&profile).Error
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func (r *profileRepository) FindByName(ctx context.Context, name string) (*models.TranscriptionProfile, error) {
	var profile models.TranscriptionProfile
	err := r.db.WithContext(ctx).Where("name = ?", name).First(&profile).Error
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

// LLMConfigRepository handles LLM configuration operations
type LLMConfigRepository interface {
	Repository[models.LLMConfig]
	GetActive(ctx context.Context) (*models.LLMConfig, error)
}

type llmConfigRepository struct {
	*BaseRepository[models.LLMConfig]
}

func NewLLMConfigRepository(db *gorm.DB) LLMConfigRepository {
	return &llmConfigRepository{
		BaseRepository: NewBaseRepository[models.LLMConfig](db),
	}
}

func (r *llmConfigRepository) GetActive(ctx context.Context) (*models.LLMConfig, error) {
	var config models.LLMConfig
	err := r.db.WithContext(ctx).Where("is_active = ?", true).First(&config).Error
	if err != nil {
		return nil, err
	}
	return &config, nil
}

// SummaryRepository handles summary templates and settings
type SummaryRepository interface {
	Repository[models.SummaryTemplate]
	GetSettings(ctx context.Context) (*models.SummarySetting, error)
	SaveSettings(ctx context.Context, settings *models.SummarySetting) error
	SaveSummary(ctx context.Context, summary *models.Summary) error
	GetLatestSummary(ctx context.Context, transcriptionID string) (*models.Summary, error)
	DeleteByTranscriptionID(ctx context.Context, transcriptionID string) error
}

type summaryRepository struct {
	*BaseRepository[models.SummaryTemplate]
}

func NewSummaryRepository(db *gorm.DB) SummaryRepository {
	return &summaryRepository{
		BaseRepository: NewBaseRepository[models.SummaryTemplate](db),
	}
}

func (r *summaryRepository) GetSettings(ctx context.Context) (*models.SummarySetting, error) {
	var settings models.SummarySetting
	// Assuming singleton settings or per-user (but currently model might not have user_id)
	// If it's a singleton table:
	err := r.db.WithContext(ctx).First(&settings).Error
	if err != nil {
		return nil, err
	}
	return &settings, nil
}

func (r *summaryRepository) SaveSettings(ctx context.Context, settings *models.SummarySetting) error {
	return r.db.WithContext(ctx).Save(settings).Error
}

func (r *summaryRepository) SaveSummary(ctx context.Context, summary *models.Summary) error {
	return r.db.WithContext(ctx).Create(summary).Error
}

func (r *summaryRepository) GetLatestSummary(ctx context.Context, transcriptionID string) (*models.Summary, error) {
	var summary models.Summary
	err := r.db.WithContext(ctx).Where("transcription_id = ?", transcriptionID).Order("created_at DESC").First(&summary).Error
	if err != nil {
		return nil, err
	}
	return &summary, nil
}

func (r *summaryRepository) DeleteByTranscriptionID(ctx context.Context, transcriptionID string) error {
	return r.db.WithContext(ctx).Where("transcription_id = ?", transcriptionID).Delete(&models.Summary{}).Error
}

// ChatRepository handles chat sessions and messages
type ChatRepository interface {
	Repository[models.ChatSession]
	GetSessionWithMessages(ctx context.Context, id string) (*models.ChatSession, error)
	GetSessionWithTranscription(ctx context.Context, id string) (*models.ChatSession, error)
	AddMessage(ctx context.Context, message *models.ChatMessage) error
	ListByJob(ctx context.Context, jobID string) ([]models.ChatSession, error)
	DeleteSession(ctx context.Context, id string) error
	GetMessages(ctx context.Context, sessionID string, limit int) ([]models.ChatMessage, error)
	DeleteByJobID(ctx context.Context, jobID string) error
	GetMessageCountsBySessionIDs(ctx context.Context, sessionIDs []string) (map[string]int64, error)
	GetLastMessagesBySessionIDs(ctx context.Context, sessionIDs []string) (map[string]*models.ChatMessage, error)
}

type chatRepository struct {
	*BaseRepository[models.ChatSession]
}

func NewChatRepository(db *gorm.DB) ChatRepository {
	return &chatRepository{
		BaseRepository: NewBaseRepository[models.ChatSession](db),
	}
}

func (r *chatRepository) GetSessionWithMessages(ctx context.Context, id string) (*models.ChatSession, error) {
	var session models.ChatSession
	err := r.db.WithContext(ctx).Preload("Messages").Where("id = ?", id).First(&session).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *chatRepository) GetSessionWithTranscription(ctx context.Context, id string) (*models.ChatSession, error) {
	var session models.ChatSession
	err := r.db.WithContext(ctx).Preload("Transcription").Where("id = ?", id).First(&session).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *chatRepository) AddMessage(ctx context.Context, message *models.ChatMessage) error {
	return r.db.WithContext(ctx).Create(message).Error
}

func (r *chatRepository) ListByJob(ctx context.Context, jobID string) ([]models.ChatSession, error) {
	var sessions []models.ChatSession
	err := r.db.WithContext(ctx).Where("transcription_id = ?", jobID).Order("created_at DESC").Find(&sessions).Error
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

func (r *chatRepository) DeleteSession(ctx context.Context, id string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Delete messages first
		if err := tx.Where("chat_session_id = ?", id).Delete(&models.ChatMessage{}).Error; err != nil {
			return err
		}
		// Delete session
		return tx.Delete(&models.ChatSession{}, "id = ?", id).Error
	})
}

func (r *chatRepository) DeleteByJobID(ctx context.Context, jobID string) error {
	// Find all sessions for this job
	var sessions []models.ChatSession
	if err := r.db.WithContext(ctx).Where("transcription_id = ?", jobID).Find(&sessions).Error; err != nil {
		return err
	}

	// Delete each session (which deletes messages)
	for _, session := range sessions {
		if err := r.DeleteSession(ctx, session.ID); err != nil {
			return err
		}
	}
	return nil
}

func (r *chatRepository) GetMessages(ctx context.Context, sessionID string, limit int) ([]models.ChatMessage, error) {
	var messages []models.ChatMessage
	query := r.db.WithContext(ctx).Where("chat_session_id = ?", sessionID).Order("created_at ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&messages).Error
	if err != nil {
		return nil, err
	}
	return messages, nil
}

func (r *chatRepository) GetMessageCountsBySessionIDs(ctx context.Context, sessionIDs []string) (map[string]int64, error) {
	if len(sessionIDs) == 0 {
		return make(map[string]int64), nil
	}

	type MessageCount struct {
		SessionID string `gorm:"column:session_id"`
		Count     int64  `gorm:"column:count"`
	}
	var counts []MessageCount

	err := r.db.WithContext(ctx).Model(&models.ChatMessage{}).
		Select("chat_session_id as session_id, COUNT(*) as count").
		Where("chat_session_id IN ?", sessionIDs).
		Group("chat_session_id").
		Scan(&counts).Error
	if err != nil {
		return nil, err
	}

	result := make(map[string]int64)
	for _, c := range counts {
		result[c.SessionID] = c.Count
	}
	return result, nil
}

func (r *chatRepository) GetLastMessagesBySessionIDs(ctx context.Context, sessionIDs []string) (map[string]*models.ChatMessage, error) {
	if len(sessionIDs) == 0 {
		return make(map[string]*models.ChatMessage), nil
	}

	var lastMessages []models.ChatMessage
	err := r.db.WithContext(ctx).Where(`id IN (
		SELECT id FROM chat_messages cm1
		WHERE cm1.chat_session_id IN ? 
		AND cm1.created_at = (
			SELECT MAX(cm2.created_at) 
			FROM chat_messages cm2 
			WHERE cm2.chat_session_id = cm1.chat_session_id
		)
	)`, sessionIDs).Find(&lastMessages).Error
	if err != nil {
		return nil, err
	}

	result := make(map[string]*models.ChatMessage)
	for i := range lastMessages {
		result[lastMessages[i].ChatSessionID] = &lastMessages[i]
	}
	return result, nil
}

// NoteRepository handles notes
type NoteRepository interface {
	Repository[models.Note]
	ListByJob(ctx context.Context, jobID string) ([]models.Note, error)
	DeleteByTranscriptionID(ctx context.Context, transcriptionID string) error
}

type noteRepository struct {
	*BaseRepository[models.Note]
}

func NewNoteRepository(db *gorm.DB) NoteRepository {
	return &noteRepository{
		BaseRepository: NewBaseRepository[models.Note](db),
	}
}

func (r *noteRepository) ListByJob(ctx context.Context, jobID string) ([]models.Note, error) {
	var notes []models.Note
	err := r.db.WithContext(ctx).Where("transcription_id = ?", jobID).Order("created_at DESC").Find(&notes).Error
	if err != nil {
		return nil, err
	}
	return notes, nil
}

func (r *noteRepository) DeleteByTranscriptionID(ctx context.Context, transcriptionID string) error {
	return r.db.WithContext(ctx).Where("transcription_id = ?", transcriptionID).Delete(&models.Note{}).Error
}

// SpeakerMappingRepository handles speaker mappings
type SpeakerMappingRepository interface {
	Repository[models.SpeakerMapping]
	ListByJob(ctx context.Context, jobID string) ([]models.SpeakerMapping, error)
	UpdateMappings(ctx context.Context, jobID string, mappings []models.SpeakerMapping) error
	DeleteByJobID(ctx context.Context, jobID string) error
}

type speakerMappingRepository struct {
	*BaseRepository[models.SpeakerMapping]
}

func NewSpeakerMappingRepository(db *gorm.DB) SpeakerMappingRepository {
	return &speakerMappingRepository{
		BaseRepository: NewBaseRepository[models.SpeakerMapping](db),
	}
}

func (r *speakerMappingRepository) ListByJob(ctx context.Context, jobID string) ([]models.SpeakerMapping, error) {
	var mappings []models.SpeakerMapping
	err := r.db.WithContext(ctx).Where("transcription_job_id = ?", jobID).Find(&mappings).Error
	if err != nil {
		return nil, err
	}
	return mappings, nil
}

func (r *speakerMappingRepository) DeleteByJobID(ctx context.Context, jobID string) error {
	return r.db.WithContext(ctx).Where("transcription_job_id = ?", jobID).Delete(&models.SpeakerMapping{}).Error
}

func (r *speakerMappingRepository) UpdateMappings(ctx context.Context, jobID string, mappings []models.SpeakerMapping) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Delete existing mappings for this job
		if err := tx.Where("transcription_job_id = ?", jobID).Delete(&models.SpeakerMapping{}).Error; err != nil {
			return err
		}

		// Create new mappings
		if len(mappings) > 0 {
			if err := tx.Create(&mappings).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// RefreshTokenRepository handles refresh token operations
type RefreshTokenRepository interface {
	Create(ctx context.Context, token *models.RefreshToken) error
	FindByHash(ctx context.Context, hash string) (*models.RefreshToken, error)
	Revoke(ctx context.Context, id uint) error
	RevokeByHash(ctx context.Context, hash string) error
	Rotate(ctx context.Context, currentHash string, replacement *models.RefreshToken, now time.Time) (uint, error)
	RevokeAllForUser(ctx context.Context, userID uint) error
}

type refreshTokenRepository struct {
	db *gorm.DB
}

func NewRefreshTokenRepository(db *gorm.DB) RefreshTokenRepository {
	return &refreshTokenRepository{db: db}
}

func (r *refreshTokenRepository) Create(ctx context.Context, token *models.RefreshToken) error {
	if token.FamilyID == "" {
		token.FamilyID = uuid.NewString()
	}
	return r.db.WithContext(ctx).Create(token).Error
}

func (r *refreshTokenRepository) FindByHash(ctx context.Context, hash string) (*models.RefreshToken, error) {
	var token models.RefreshToken
	err := r.db.WithContext(ctx).Where("hashed = ?", hash).First(&token).Error
	if err != nil {
		return nil, err
	}
	return &token, nil
}

func (r *refreshTokenRepository) Revoke(ctx context.Context, id uint) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&models.RefreshToken{}).Where("id = ?", id).
		Updates(map[string]interface{}{"revoked": true, "revoked_at": &now}).Error
}

func (r *refreshTokenRepository) RevokeByHash(ctx context.Context, hash string) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&models.RefreshToken{}).Where("hashed = ?", hash).
		Updates(map[string]interface{}{"revoked": true, "revoked_at": &now}).Error
}

func (r *refreshTokenRepository) Rotate(ctx context.Context, currentHash string, replacement *models.RefreshToken, now time.Time) (uint, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		var userID uint
		replayed := false
		candidate := *replacement
		candidate.ID = 0
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current models.RefreshToken
			if err := tx.Where("hashed = ?", currentHash).First(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrRefreshTokenInvalid
				}
				return err
			}

			familyID := current.FamilyID
			if familyID == "" {
				familyID = uuid.NewString()
			}
			if current.Revoked {
				replayed = true
				return tx.Model(&models.RefreshToken{}).Where("family_id = ? OR id = ?", familyID, current.ID).
					Updates(map[string]interface{}{"revoked": true, "revoked_at": &now, "family_id": familyID}).Error
			}
			if !current.ExpiresAt.After(now) {
				return ErrRefreshTokenInvalid
			}

			result := tx.Model(&models.RefreshToken{}).
				Where("id = ? AND revoked = ?", current.ID, false).
				Updates(map[string]interface{}{
					"revoked":          true,
					"revoked_at":       &now,
					"replaced_by_hash": candidate.Hashed,
					"family_id":        familyID,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				replayed = true
				return tx.Model(&models.RefreshToken{}).Where("family_id = ?", familyID).
					Updates(map[string]interface{}{"revoked": true, "revoked_at": &now}).Error
			}

			candidate.UserID = current.UserID
			candidate.FamilyID = familyID
			if err := tx.Create(&candidate).Error; err != nil {
				return err
			}
			userID = current.UserID
			return nil
		})
		if err == nil {
			if replayed {
				return 0, ErrRefreshTokenReplay
			}
			*replacement = candidate
			return userID, nil
		}
		if !isTransientDatabaseLock(err) {
			return 0, err
		}
		lastErr = err
		if err := waitForDatabaseRetry(ctx, attempt); err != nil {
			return 0, err
		}
	}
	return 0, fmt.Errorf("rotate refresh token after database contention: %w", lastErr)
}

func isTransientDatabaseLock(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "sqlite_busy")
}

func waitForDatabaseRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *refreshTokenRepository) RevokeAllForUser(ctx context.Context, userID uint) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&models.RefreshToken{}).
		Where("user_id = ? AND revoked = ?", userID, false).
		Updates(map[string]interface{}{"revoked": true, "revoked_at": &now}).Error
}
