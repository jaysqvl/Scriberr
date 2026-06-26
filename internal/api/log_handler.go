package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetJobLogs returns the transcription logs for a specific job
// @Summary Get transcription logs
// @Description Get the raw transcription logs for a job
// @Tags transcription
// @Produce json
// @Param id path string true "Job ID"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /transcription/{id}/logs [get]
func (h *Handler) GetJobLogs(c *gin.Context) {
	jobID := c.Param("id")

	if execution, err := h.jobRepo.FindLatestExecution(c.Request.Context(), jobID); err == nil && execution.LogPath != nil {
		h.writeRunLogResponse(c, jobID, execution.ID, execution.LogPath)
		return
	} else if err != nil && err != gorm.ErrRecordNotFound {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to get latest run logs: %v", err)})
		return
	}

	// Construct path to log file
	logPath := filepath.Join(h.config.TranscriptsDir, jobID, "transcription.log")
	h.writeRunLogResponse(c, jobID, "", &logPath)
}

func (h *Handler) writeRunLogResponse(c *gin.Context, jobID, runID string, logPath *string) {
	if logPath == nil || *logPath == "" {
		c.JSON(http.StatusOK, gin.H{
			"job_id":    jobID,
			"run_id":    runID,
			"available": false,
			"content":   "",
			"message":   "No logs available for this run",
		})
		return
	}

	if !h.isTranscriptLogPath(*logPath) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Log path is outside the transcript directory"})
		return
	}

	// Check if file exists
	exists, err := h.fileService.FileExists(*logPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to check logs: %v", err)})
		return
	}
	if !exists {
		// Return graceful empty response instead of 404
		c.JSON(http.StatusOK, gin.H{
			"job_id":    jobID,
			"run_id":    runID,
			"available": false,
			"content":   "",
			"message":   "No logs available for this run",
		})
		return
	}

	// Read file content
	content, err := h.fileService.ReadFile(*logPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to read logs: %v", err)})
		return
	}

	// Return as JSON with content for consistency
	c.JSON(http.StatusOK, gin.H{
		"job_id":    jobID,
		"run_id":    runID,
		"available": true,
		"content":   string(content),
	})
}

func (h *Handler) isTranscriptLogPath(logPath string) bool {
	basePath, err := filepath.Abs(h.config.TranscriptsDir)
	if err != nil {
		return false
	}
	targetPath, err := filepath.Abs(logPath)
	if err != nil {
		return false
	}
	relativePath, err := filepath.Rel(basePath, targetPath)
	if err != nil {
		return false
	}
	return relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) && !filepath.IsAbs(relativePath)
}
