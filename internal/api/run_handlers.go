package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"scriberr/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h *Handler) preserveCurrentRunSnapshot(ctx context.Context, job *models.TranscriptionJob) error {
	if job == nil || job.Transcript == nil {
		return nil
	}

	legacyLogPath := filepath.Join(h.config.TranscriptsDir, job.ID, "transcription.log")
	var logPath *string
	if exists, err := h.fileService.FileExists(legacyLogPath); err == nil && exists {
		logPath = &legacyLogPath
	}

	execution, err := h.jobRepo.FindLatestCompletedExecution(ctx, job.ID)
	if err == nil {
		changed := false
		if execution.Transcript == nil {
			execution.Transcript = job.Transcript
			changed = true
		}
		if execution.LogPath == nil && logPath != nil {
			execution.LogPath = logPath
			changed = true
		}
		if changed {
			return h.jobRepo.UpdateExecution(ctx, execution)
		}
		return nil
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}

	completedAt := job.UpdatedAt
	if completedAt.IsZero() {
		completedAt = job.CreatedAt
	}
	execution = &models.TranscriptionJobExecution{
		TranscriptionJobID: job.ID,
		StartedAt:          job.CreatedAt,
		CompletedAt:        &completedAt,
		ActualParameters:   job.Parameters,
		Status:             models.StatusCompleted,
		Transcript:         job.Transcript,
		LogPath:            logPath,
	}
	execution.CalculateProcessingDuration()
	return h.jobRepo.CreateExecution(ctx, execution)
}

// ListJobRuns returns every recorded execution for a transcription job.
// @Summary List transcription runs
// @Description List every transcription attempt for a job, including timing, status, and captured parameter metadata
// @Tags transcription
// @Produce json
// @Param id path string true "Job ID"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/transcription/{id}/runs [get]
// @Security ApiKeyAuth
// @Security BearerAuth
func (h *Handler) ListJobRuns(c *gin.Context) {
	jobID := c.Param("id")
	job, err := h.jobRepo.FindByID(c.Request.Context(), jobID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get job"})
		return
	}

	if err := h.preserveCurrentRunSnapshot(c.Request.Context(), job); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare run history"})
		return
	}

	executions, err := h.jobRepo.ListExecutionsByJobID(c.Request.Context(), jobID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list runs"})
		return
	}

	activeRunID := ""
	pinnedRunID := ""
	activeRunPinned := false
	if job.PinnedExecutionID != nil {
		pinnedRunID = *job.PinnedExecutionID
	}
	if active, pinned, err := h.resolveActiveExecution(c.Request.Context(), job); err == nil {
		activeRunID = active.ID
		activeRunPinned = pinned
	} else if err != gorm.ErrRecordNotFound {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve active run"})
		return
	}

	runs := make([]gin.H, 0, len(executions))
	for index := len(executions) - 1; index >= 0; index-- {
		execution := executions[index]
		runs = append(runs, h.executionRunResponse(execution, index+1))
	}

	c.JSON(http.StatusOK, gin.H{
		"job_id":            jobID,
		"active_run_id":     activeRunID,
		"pinned_run_id":     pinnedRunID,
		"active_run_pinned": activeRunPinned,
		"runs":              runs,
	})
}

// SetActiveRun pins one completed run as the active transcript source.
// @Summary Set active transcription run
// @Description Pin a completed run as the active transcript source for a transcription job
// @Tags transcription
// @Produce json
// @Param id path string true "Job ID"
// @Param run_id path string true "Run ID"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/transcription/{id}/runs/{run_id}/active [post]
// @Security ApiKeyAuth
// @Security BearerAuth
func (h *Handler) SetActiveRun(c *gin.Context) {
	jobID := c.Param("id")
	runID := c.Param("run_id")

	if _, err := h.jobRepo.FindByID(c.Request.Context(), jobID); err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get job"})
		return
	}

	execution, err := h.jobRepo.FindExecution(c.Request.Context(), jobID, runID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Run not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get run"})
		return
	}

	if execution.Status != models.StatusCompleted {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only completed runs can be active"})
		return
	}
	if execution.Transcript == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Run has no transcript to make active"})
		return
	}

	pinnedRunID := execution.ID
	if err := h.jobRepo.SetPinnedExecution(c.Request.Context(), jobID, &pinnedRunID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to set active run"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"job_id":            jobID,
		"active_run_id":     execution.ID,
		"pinned_run_id":     execution.ID,
		"active_run_pinned": true,
	})
}

// ClearActiveRun clears the pinned active run and returns to latest-completed fallback.
// @Summary Clear active transcription run
// @Description Clear a pinned active run so the latest completed run becomes active again
// @Tags transcription
// @Produce json
// @Param id path string true "Job ID"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/transcription/{id}/runs/active [delete]
// @Security ApiKeyAuth
// @Security BearerAuth
func (h *Handler) ClearActiveRun(c *gin.Context) {
	jobID := c.Param("id")

	job, err := h.jobRepo.FindByID(c.Request.Context(), jobID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get job"})
		return
	}

	if err := h.jobRepo.SetPinnedExecution(c.Request.Context(), jobID, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear active run"})
		return
	}
	job.PinnedExecutionID = nil

	activeRunID := ""
	if active, _, err := h.resolveActiveExecution(c.Request.Context(), job); err == nil {
		activeRunID = active.ID
	} else if err != gorm.ErrRecordNotFound {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve active run"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"job_id":            jobID,
		"active_run_id":     activeRunID,
		"pinned_run_id":     "",
		"active_run_pinned": false,
	})
}

// GetRunTranscript returns the transcript captured for one run.
// @Summary Get run transcript
// @Description Get the transcript snapshot captured for a specific transcription run
// @Tags transcription
// @Produce json
// @Param id path string true "Job ID"
// @Param run_id path string true "Run ID"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/transcription/{id}/runs/{run_id}/transcript [get]
// @Security ApiKeyAuth
// @Security BearerAuth
func (h *Handler) GetRunTranscript(c *gin.Context) {
	jobID := c.Param("id")
	runID := c.Param("run_id")

	execution, err := h.jobRepo.FindExecution(c.Request.Context(), jobID, runID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Run not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get run"})
		return
	}

	if execution.Transcript == nil {
		c.JSON(http.StatusOK, gin.H{
			"job_id":     jobID,
			"run_id":     runID,
			"available":  false,
			"transcript": nil,
			"message":    "No transcript captured for this run",
		})
		return
	}

	transcript, err := parseTranscriptPayload(*execution.Transcript)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse run transcript"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"job_id":     jobID,
		"run_id":     runID,
		"available":  true,
		"transcript": transcript,
	})
}

// GetRunLogs returns logs captured for one run.
// @Summary Get run logs
// @Description Get the log snapshot captured for a specific transcription run
// @Tags transcription
// @Produce json
// @Param id path string true "Job ID"
// @Param run_id path string true "Run ID"
// @Success 200 {object} map[string]interface{}
// @Failure 403 {object} map[string]string
// @Failure 404 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/transcription/{id}/runs/{run_id}/logs [get]
// @Security ApiKeyAuth
// @Security BearerAuth
func (h *Handler) GetRunLogs(c *gin.Context) {
	jobID := c.Param("id")
	runID := c.Param("run_id")

	execution, err := h.jobRepo.FindExecution(c.Request.Context(), jobID, runID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Run not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get run"})
		return
	}

	h.writeRunLogResponse(c, jobID, runID, execution.LogPath)
}

func (h *Handler) resolveActiveExecution(ctx context.Context, job *models.TranscriptionJob) (*models.TranscriptionJobExecution, bool, error) {
	if job == nil {
		return nil, false, gorm.ErrRecordNotFound
	}

	if job.PinnedExecutionID != nil && *job.PinnedExecutionID != "" {
		execution, err := h.jobRepo.FindExecution(ctx, job.ID, *job.PinnedExecutionID)
		if err != nil && err != gorm.ErrRecordNotFound {
			return nil, false, err
		}
		if err == nil && execution.Status == models.StatusCompleted {
			return execution, true, nil
		}
	}

	execution, err := h.jobRepo.FindLatestCompletedExecution(ctx, job.ID)
	if err != nil {
		return nil, false, err
	}
	return execution, false, nil
}

func (h *Handler) executionRunResponse(execution models.TranscriptionJobExecution, runNumber int) gin.H {
	return gin.H{
		"id":                   execution.ID,
		"run_number":           runNumber,
		"transcription_job_id": execution.TranscriptionJobID,
		"started_at":           execution.StartedAt,
		"completed_at":         execution.CompletedAt,
		"processing_duration":  execution.ProcessingDuration,
		"actual_parameters":    execution.ActualParameters,
		"status":               execution.Status,
		"error_message":        execution.ErrorMessage,
		"created_at":           execution.CreatedAt,
		"updated_at":           execution.UpdatedAt,
		"has_transcript":       execution.Transcript != nil,
		"has_logs":             execution.LogPath != nil,
	}
}

func parseTranscriptPayload(raw string) (interface{}, error) {
	var transcript interface{}
	if err := json.Unmarshal([]byte(raw), &transcript); err != nil {
		return nil, fmt.Errorf("failed to parse transcript: %w", err)
	}
	return transcript, nil
}
