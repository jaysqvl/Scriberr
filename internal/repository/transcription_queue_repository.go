package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"scriberr/internal/models"

	"gorm.io/gorm"
)

var (
	ErrQueueItemNotFound  = errors.New("queued run not found")
	ErrQueueItemNotQueued = errors.New("run is no longer waiting in the queue")
	ErrQueueOrderMismatch = errors.New("ordered_ids must contain every waiting run exactly once")
	ErrQueueLimitReached  = errors.New("a maximum of 50 active or waiting runs is allowed per audio file")
)

const MaxSequentialRunsPerJob int64 = 50

// TranscriptionQueueRepository persists and atomically transitions sequential
// transcription runs. The methods that touch both a queue item and its parent
// job do so in one database transaction so workers never observe half-applied
// parameters.
type TranscriptionQueueRepository interface {
	Append(ctx context.Context, jobID string, items []models.TranscriptionQueueItem) error
	List(ctx context.Context, jobID string, includeTerminal bool) ([]models.TranscriptionQueueItem, error)
	FindByID(ctx context.Context, jobID, itemID string) (*models.TranscriptionQueueItem, error)
	ReorderQueued(ctx context.Context, jobID string, orderedIDs []string) error
	CancelQueued(ctx context.Context, jobID, itemID string) error
	ClearQueued(ctx context.Context, jobID string) (int64, error)
	PromoteNext(ctx context.Context, jobID string) (*models.TranscriptionQueueItem, error)
	ClaimPending(ctx context.Context, jobID, itemID string) (*models.TranscriptionQueueItem, bool, error)
	FindPending(ctx context.Context, jobID string) (*models.TranscriptionQueueItem, error)
	FindActive(ctx context.Context, jobID string) (*models.TranscriptionQueueItem, error)
	ListProcessing(ctx context.Context) ([]models.TranscriptionQueueItem, error)
	MarkActiveCancelled(ctx context.Context, jobID, itemID, reason string) error
	Finalize(ctx context.Context, jobID, itemID string, status models.TranscriptionQueueStatus, errorMessage string, executionID *string) error
	FailInterrupted(ctx context.Context, reason string) ([]string, error)
	ListJobIDsWithItems(ctx context.Context) ([]string, error)
	ListJobIDsWithQueued(ctx context.Context) ([]string, error)
	DeleteByJobID(ctx context.Context, jobID string) error
}

type transcriptionQueueRepository struct {
	db *gorm.DB

	// SQLite serializes writers, but this lock also makes position allocation and
	// promotion deterministic before the transaction reaches the driver.
	mu sync.Mutex
}

func NewTranscriptionQueueRepository(db *gorm.DB) TranscriptionQueueRepository {
	return &transcriptionQueueRepository{db: db}
}

func (r *transcriptionQueueRepository) Append(ctx context.Context, jobID string, items []models.TranscriptionQueueItem) error {
	if len(items) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job models.TranscriptionJob
		if err := tx.First(&job, "id = ?", jobID).Error; err != nil {
			return err
		}
		var nonTerminalCount int64
		if err := tx.Model(&models.TranscriptionQueueItem{}).
			Where("transcription_job_id = ? AND status IN ?", jobID, []models.TranscriptionQueueStatus{
				models.QueueStatusQueued,
				models.QueueStatusPending,
				models.QueueStatusProcessing,
			}).Count(&nonTerminalCount).Error; err != nil {
			return err
		}
		if nonTerminalCount+int64(len(items)) > MaxSequentialRunsPerJob {
			return ErrQueueLimitReached
		}

		var maxPosition int
		if err := tx.Model(&models.TranscriptionQueueItem{}).
			Where("transcription_job_id = ? AND status = ?", jobID, models.QueueStatusQueued).
			Select("COALESCE(MAX(position), 0)").
			Scan(&maxPosition).Error; err != nil {
			return err
		}

		for index := range items {
			items[index].TranscriptionJobID = jobID
			items[index].Status = models.QueueStatusQueued
			items[index].Position = maxPosition + index + 1
			items[index].ExecutionID = nil
			items[index].ErrorMessage = nil
			items[index].StartedAt = nil
			items[index].CompletedAt = nil
			if err := tx.Create(&items[index]).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

func (r *transcriptionQueueRepository) List(ctx context.Context, jobID string, includeTerminal bool) ([]models.TranscriptionQueueItem, error) {
	items := make([]models.TranscriptionQueueItem, 0)
	db := r.db.WithContext(ctx).Where("transcription_job_id = ?", jobID)
	if !includeTerminal {
		db = db.Where("status IN ?", []models.TranscriptionQueueStatus{
			models.QueueStatusQueued,
			models.QueueStatusPending,
			models.QueueStatusProcessing,
		})
	}
	err := db.
		Order("CASE status WHEN 'processing' THEN 0 WHEN 'pending' THEN 1 WHEN 'queued' THEN 2 ELSE 3 END ASC").
		Order("position ASC").
		Order("queued_at ASC").
		Order("id ASC").
		Find(&items).Error
	return items, err
}

func (r *transcriptionQueueRepository) FindByID(ctx context.Context, jobID, itemID string) (*models.TranscriptionQueueItem, error) {
	var item models.TranscriptionQueueItem
	err := r.db.WithContext(ctx).
		Where("id = ? AND transcription_job_id = ?", itemID, jobID).
		First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *transcriptionQueueRepository) ReorderQueued(ctx context.Context, jobID string, orderedIDs []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var queued []models.TranscriptionQueueItem
		if err := tx.Where("transcription_job_id = ? AND status = ?", jobID, models.QueueStatusQueued).
			Order("position ASC").Find(&queued).Error; err != nil {
			return err
		}
		if len(queued) != len(orderedIDs) {
			return ErrQueueOrderMismatch
		}

		available := make(map[string]struct{}, len(queued))
		for _, item := range queued {
			available[item.ID] = struct{}{}
		}
		seen := make(map[string]struct{}, len(orderedIDs))
		for _, id := range orderedIDs {
			if _, ok := available[id]; !ok {
				return ErrQueueOrderMismatch
			}
			if _, duplicate := seen[id]; duplicate {
				return ErrQueueOrderMismatch
			}
			seen[id] = struct{}{}
		}

		for index, id := range orderedIDs {
			result := tx.Model(&models.TranscriptionQueueItem{}).
				Where("id = ? AND transcription_job_id = ? AND status = ?", id, jobID, models.QueueStatusQueued).
				Update("position", index+1)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrQueueOrderMismatch
			}
		}
		return nil
	})
}

func (r *transcriptionQueueRepository) CancelQueued(ctx context.Context, jobID, itemID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		reason := "Run cancelled before processing"
		result := tx.Model(&models.TranscriptionQueueItem{}).
			Where("id = ? AND transcription_job_id = ? AND status = ?", itemID, jobID, models.QueueStatusQueued).
			Updates(map[string]any{
				"status":          models.QueueStatusCancelled,
				"position":        0,
				"error_message":   reason,
				"completed_at":    &now,
				"parameters_json": gorm.Expr("json_remove(parameters_json, '$.hf_token', '$.api_key')"),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			var count int64
			if err := tx.Model(&models.TranscriptionQueueItem{}).
				Where("id = ? AND transcription_job_id = ?", itemID, jobID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return ErrQueueItemNotFound
			}
			return ErrQueueItemNotQueued
		}
		return normalizeQueuedPositions(tx, jobID)
	})
}

func (r *transcriptionQueueRepository) ClearQueued(ctx context.Context, jobID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var cleared int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		reason := "Run cancelled before processing"
		result := tx.Model(&models.TranscriptionQueueItem{}).
			Where("transcription_job_id = ? AND status = ?", jobID, models.QueueStatusQueued).
			Updates(map[string]any{
				"status":          models.QueueStatusCancelled,
				"position":        0,
				"error_message":   reason,
				"completed_at":    &now,
				"parameters_json": gorm.Expr("json_remove(parameters_json, '$.hf_token', '$.api_key')"),
			})
		cleared = result.RowsAffected
		return result.Error
	})
	return cleared, err
}

// PromoteNext atomically makes the first waiting item the exact pending run
// and applies its immutable parameters to the audio container. Legacy pending
// or processing jobs are left alone; their worker will call this again after
// they finish.
func (r *transcriptionQueueRepository) PromoteNext(ctx context.Context, jobID string) (*models.TranscriptionQueueItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var promoted *models.TranscriptionQueueItem
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var activeCount int64
		if err := tx.Model(&models.TranscriptionQueueItem{}).
			Where("transcription_job_id = ? AND status IN ?", jobID, []models.TranscriptionQueueStatus{
				models.QueueStatusPending,
				models.QueueStatusProcessing,
			}).Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount > 0 {
			return nil
		}

		var job models.TranscriptionJob
		if err := tx.First(&job, "id = ?", jobID).Error; err != nil {
			return err
		}
		if job.Status == models.StatusPending || job.Status == models.StatusProcessing {
			return nil
		}

		var item models.TranscriptionQueueItem
		if err := tx.Where("transcription_job_id = ? AND status = ?", jobID, models.QueueStatusQueued).
			Order("position ASC, queued_at ASC, id ASC").First(&item).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}

		result := tx.Model(&models.TranscriptionQueueItem{}).
			Where("id = ? AND transcription_job_id = ? AND status = ?", item.ID, jobID, models.QueueStatusQueued).
			Updates(map[string]any{"status": models.QueueStatusPending, "position": 0})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("promote queued run %s: %w", item.ID, ErrQueueItemNotQueued)
		}
		if err := normalizeQueuedPositions(tx, jobID); err != nil {
			return err
		}

		job.Parameters = item.Parameters
		job.Diarization = item.Parameters.Diarize
		job.Status = models.StatusPending
		job.Transcript = nil
		job.Summary = nil
		job.ErrorMessage = nil
		if err := tx.Save(&job).Error; err != nil {
			return err
		}

		item.Status = models.QueueStatusPending
		item.Position = 0
		promoted = &item
		return nil
	})
	return promoted, err
}

// ClaimPending verifies the queue-item identity as part of the claim. A stale
// in-memory channel entry can therefore never execute parameters belonging to a
// newly promoted item for the same audio file.
func (r *transcriptionQueueRepository) ClaimPending(ctx context.Context, jobID, itemID string) (*models.TranscriptionQueueItem, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var claimed *models.TranscriptionQueueItem
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item models.TranscriptionQueueItem
		if err := tx.Where("id = ? AND transcription_job_id = ? AND status = ?", itemID, jobID, models.QueueStatusPending).
			First(&item).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}

		var processingCount int64
		if err := tx.Model(&models.TranscriptionQueueItem{}).
			Where("transcription_job_id = ? AND status = ?", jobID, models.QueueStatusProcessing).
			Count(&processingCount).Error; err != nil {
			return err
		}
		if processingCount > 0 {
			return nil
		}

		var job models.TranscriptionJob
		if err := tx.Where("id = ? AND status = ?", jobID, models.StatusPending).First(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}

		now := time.Now()
		result := tx.Model(&models.TranscriptionQueueItem{}).
			Where("id = ? AND transcription_job_id = ? AND status = ?", itemID, jobID, models.QueueStatusPending).
			Updates(map[string]any{"status": models.QueueStatusProcessing, "started_at": &now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}

		// Reapply the exact item's parameters during the same claim transaction.
		// This both repairs any accidental parent-job drift and makes the worker's
		// execution contract explicit.
		job.Parameters = item.Parameters
		job.Diarization = item.Parameters.Diarize
		job.Status = models.StatusProcessing
		if err := tx.Save(&job).Error; err != nil {
			return err
		}

		item.Status = models.QueueStatusProcessing
		item.StartedAt = &now
		claimed = &item
		return nil
	})
	return claimed, claimed != nil, err
}

func (r *transcriptionQueueRepository) FindPending(ctx context.Context, jobID string) (*models.TranscriptionQueueItem, error) {
	var item models.TranscriptionQueueItem
	err := r.db.WithContext(ctx).
		Where("transcription_job_id = ? AND status = ?", jobID, models.QueueStatusPending).
		Order("queued_at ASC, id ASC").First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *transcriptionQueueRepository) FindActive(ctx context.Context, jobID string) (*models.TranscriptionQueueItem, error) {
	var item models.TranscriptionQueueItem
	err := r.db.WithContext(ctx).
		Where("transcription_job_id = ? AND status IN ?", jobID, []models.TranscriptionQueueStatus{
			models.QueueStatusPending,
			models.QueueStatusProcessing,
		}).
		Order("CASE status WHEN 'processing' THEN 0 ELSE 1 END ASC").
		Order("queued_at ASC, id ASC").First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *transcriptionQueueRepository) ListProcessing(ctx context.Context) ([]models.TranscriptionQueueItem, error) {
	items := make([]models.TranscriptionQueueItem, 0)
	err := r.db.WithContext(ctx).Where("status = ?", models.QueueStatusProcessing).
		Order("started_at ASC, id ASC").Find(&items).Error
	return items, err
}

func (r *transcriptionQueueRepository) MarkActiveCancelled(ctx context.Context, jobID, itemID, reason string) error {
	if itemID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		result := tx.Model(&models.TranscriptionQueueItem{}).
			Where("id = ? AND transcription_job_id = ? AND status IN ?", itemID, jobID, []models.TranscriptionQueueStatus{
				models.QueueStatusPending,
				models.QueueStatusProcessing,
			}).Updates(map[string]any{
			"status":          models.QueueStatusCancelled,
			"position":        0,
			"error_message":   reason,
			"completed_at":    &now,
			"parameters_json": gorm.Expr("json_remove(parameters_json, '$.hf_token', '$.api_key')"),
		})
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		return tx.Model(&models.TranscriptionJob{}).Where("id = ?", jobID).Updates(map[string]any{
			"status":        models.StatusFailed,
			"error_message": reason,
		}).Error
	})
}

func (r *transcriptionQueueRepository) Finalize(ctx context.Context, jobID, itemID string, status models.TranscriptionQueueStatus, errorMessage string, executionID *string) error {
	if itemID == "" {
		return nil
	}
	if status != models.QueueStatusCompleted && status != models.QueueStatusFailed && status != models.QueueStatusCancelled {
		return fmt.Errorf("invalid terminal queue status %q", status)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		updates := map[string]any{
			"status":          status,
			"position":        0,
			"completed_at":    &now,
			"execution_id":    executionID,
			"parameters_json": gorm.Expr("json_remove(parameters_json, '$.hf_token', '$.api_key')"),
		}
		if errorMessage != "" {
			updates["error_message"] = errorMessage
		} else {
			updates["error_message"] = nil
		}
		result := tx.Model(&models.TranscriptionQueueItem{}).
			Where("id = ? AND transcription_job_id = ? AND status IN ?", itemID, jobID, []models.TranscriptionQueueStatus{
				models.QueueStatusPending,
				models.QueueStatusProcessing,
			}).Updates(updates)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}

		jobUpdates := map[string]any{"error_message": nil}
		if status == models.QueueStatusCompleted {
			jobUpdates["status"] = models.StatusCompleted
		} else {
			jobUpdates["status"] = models.StatusFailed
			if errorMessage != "" {
				jobUpdates["error_message"] = errorMessage
			}
		}
		return tx.Model(&models.TranscriptionJob{}).Where("id = ?", jobID).Updates(jobUpdates).Error
	})
}

func (r *transcriptionQueueRepository) FailInterrupted(ctx context.Context, reason string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	jobIDs := make([]string, 0)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.TranscriptionQueueItem{}).
			Where("status = ?", models.QueueStatusProcessing).
			Distinct("transcription_job_id").Pluck("transcription_job_id", &jobIDs).Error; err != nil {
			return err
		}
		if len(jobIDs) == 0 {
			return nil
		}
		now := time.Now()
		return tx.Model(&models.TranscriptionQueueItem{}).
			Where("status = ?", models.QueueStatusProcessing).
			Updates(map[string]any{
				"status":          models.QueueStatusFailed,
				"position":        0,
				"error_message":   reason,
				"completed_at":    &now,
				"parameters_json": gorm.Expr("json_remove(parameters_json, '$.hf_token', '$.api_key')"),
			}).Error
	})
	return jobIDs, err
}

func (r *transcriptionQueueRepository) ListJobIDsWithQueued(ctx context.Context) ([]string, error) {
	jobIDs := make([]string, 0)
	err := r.db.WithContext(ctx).Model(&models.TranscriptionQueueItem{}).
		Where("status = ?", models.QueueStatusQueued).
		Distinct("transcription_job_id").
		Order("transcription_job_id ASC").
		Pluck("transcription_job_id", &jobIDs).Error
	return jobIDs, err
}

func (r *transcriptionQueueRepository) ListJobIDsWithItems(ctx context.Context) ([]string, error) {
	jobIDs := make([]string, 0)
	err := r.db.WithContext(ctx).Model(&models.TranscriptionQueueItem{}).
		Distinct("transcription_job_id").
		Order("transcription_job_id ASC").
		Pluck("transcription_job_id", &jobIDs).Error
	return jobIDs, err
}

func (r *transcriptionQueueRepository) DeleteByJobID(ctx context.Context, jobID string) error {
	return r.db.WithContext(ctx).Where("transcription_job_id = ?", jobID).
		Delete(&models.TranscriptionQueueItem{}).Error
}

func normalizeQueuedPositions(tx *gorm.DB, jobID string) error {
	var items []models.TranscriptionQueueItem
	if err := tx.Where("transcription_job_id = ? AND status = ?", jobID, models.QueueStatusQueued).
		Order("position ASC, queued_at ASC, id ASC").Find(&items).Error; err != nil {
		return err
	}
	for index, item := range items {
		position := index + 1
		if item.Position == position {
			continue
		}
		if err := tx.Model(&models.TranscriptionQueueItem{}).Where("id = ?", item.ID).
			Update("position", position).Error; err != nil {
			return err
		}
	}
	return nil
}
