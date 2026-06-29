package tests

import (
	"encoding/json"
	"net/http"
	"time"

	"scriberr/internal/models"

	"github.com/stretchr/testify/assert"
)

func (suite *APIHandlerTestSuite) TestRunHistoryEndpointsReturnExecutionTranscript() {
	job := suite.helper.CreateTestTranscriptionJob(suite.T(), "run history")
	transcript := `{"text":"hello from run one","segments":[{"start":0,"end":1,"text":"hello from run one"}]}`
	completedAt := time.Now()
	logPath := "/etc/passwd"
	execution := &models.TranscriptionJobExecution{
		TranscriptionJobID: job.ID,
		StartedAt:          completedAt.Add(-2 * time.Minute),
		CompletedAt:        &completedAt,
		ActualParameters: models.WhisperXParams{
			ModelFamily:     "nvidia_canary",
			Device:          "cuda",
			BatchSize:       1,
			NvidiaPrecision: "bfloat16",
		},
		Status:     models.StatusCompleted,
		Transcript: &transcript,
		LogPath:    &logPath,
	}
	execution.CalculateProcessingDuration()
	assert.NoError(suite.T(), suite.helper.DB.Create(execution).Error)

	w := suite.makeAuthenticatedRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/runs", nil, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var listResponse struct {
		Runs []struct {
			ID            string `json:"id"`
			RunNumber     int    `json:"run_number"`
			HasTranscript bool   `json:"has_transcript"`
			HasLogs       bool   `json:"has_logs"`
		} `json:"runs"`
	}
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &listResponse))
	assert.Len(suite.T(), listResponse.Runs, 1)
	assert.Equal(suite.T(), execution.ID, listResponse.Runs[0].ID)
	assert.Equal(suite.T(), 1, listResponse.Runs[0].RunNumber)
	assert.True(suite.T(), listResponse.Runs[0].HasTranscript)
	assert.True(suite.T(), listResponse.Runs[0].HasLogs)

	var rawListResponse map[string]interface{}
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &rawListResponse))
	runMetadata := rawListResponse["runs"].([]interface{})[0].(map[string]interface{})
	assert.NotContains(suite.T(), runMetadata, "log_path")

	w = suite.makeAuthenticatedRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/runs/"+execution.ID+"/transcript", nil, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var transcriptResponse struct {
		Available  bool `json:"available"`
		Transcript struct {
			Text string `json:"text"`
		} `json:"transcript"`
	}
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &transcriptResponse))
	assert.True(suite.T(), transcriptResponse.Available)
	assert.Equal(suite.T(), "hello from run one", transcriptResponse.Transcript.Text)

	w = suite.makeAuthenticatedRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/runs/"+execution.ID+"/logs", nil, true)
	assert.Equal(suite.T(), http.StatusForbidden, w.Code)
}

func (suite *APIHandlerTestSuite) TestRerunSnapshotsLegacyTranscriptBeforeQueueing() {
	job := suite.helper.CreateTestTranscriptionJob(suite.T(), "legacy result")
	transcript := `{"text":"old result"}`
	job.Status = models.StatusCompleted
	job.Transcript = &transcript
	job.Parameters = models.WhisperXParams{
		ModelFamily: "whisper",
		Model:       "base",
		Device:      "cpu",
		BatchSize:   8,
	}
	assert.NoError(suite.T(), suite.helper.DB.Save(job).Error)

	nextParams := models.WhisperXParams{
		ModelFamily: "nvidia_canary",
		Device:      "cuda",
		BatchSize:   1,
	}
	w := suite.makeAuthenticatedRequest(http.MethodPost, "/api/v1/transcription/"+job.ID+"/rerun", nextParams, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var executions []models.TranscriptionJobExecution
	assert.NoError(suite.T(), suite.helper.DB.Where("transcription_job_id = ?", job.ID).Find(&executions).Error)
	assert.Len(suite.T(), executions, 1)
	assert.NotNil(suite.T(), executions[0].Transcript)
	assert.Equal(suite.T(), transcript, *executions[0].Transcript)

	var updatedJob models.TranscriptionJob
	assert.NoError(suite.T(), suite.helper.DB.First(&updatedJob, "id = ?", job.ID).Error)
	assert.Equal(suite.T(), models.StatusPending, updatedJob.Status)
	assert.Nil(suite.T(), updatedJob.Transcript)
	assert.Equal(suite.T(), "nvidia_canary", updatedJob.Parameters.ModelFamily)
}

func (suite *APIHandlerTestSuite) TestListRunsBackfillsLegacyCompletedTranscript() {
	job := suite.helper.CreateTestTranscriptionJob(suite.T(), "existing completed")
	transcript := `{"text":"existing transcript"}`
	job.Status = models.StatusCompleted
	job.Transcript = &transcript
	job.Parameters = models.WhisperXParams{
		ModelFamily: "nvidia_canary",
		Device:      "cuda",
		BatchSize:   1,
	}
	assert.NoError(suite.T(), suite.helper.DB.Save(job).Error)

	w := suite.makeAuthenticatedRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/runs", nil, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var listResponse struct {
		ActiveRunID string `json:"active_run_id"`
		Runs        []struct {
			ID            string `json:"id"`
			RunNumber     int    `json:"run_number"`
			HasTranscript bool   `json:"has_transcript"`
		} `json:"runs"`
	}
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &listResponse))
	assert.Len(suite.T(), listResponse.Runs, 1)
	assert.Equal(suite.T(), 1, listResponse.Runs[0].RunNumber)
	assert.True(suite.T(), listResponse.Runs[0].HasTranscript)
	assert.Equal(suite.T(), listResponse.Runs[0].ID, listResponse.ActiveRunID)

	w = suite.makeAuthenticatedRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/runs", nil, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var executionCount int64
	assert.NoError(suite.T(), suite.helper.DB.Model(&models.TranscriptionJobExecution{}).Where("transcription_job_id = ?", job.ID).Count(&executionCount).Error)
	assert.Equal(suite.T(), int64(1), executionCount)
}

func (suite *APIHandlerTestSuite) TestActiveRunCanBePinnedAndCleared() {
	job := suite.helper.CreateTestTranscriptionJob(suite.T(), "active run selection")
	job.Status = models.StatusCompleted
	assert.NoError(suite.T(), suite.helper.DB.Save(job).Error)

	olderTranscript := `{"text":"older but better"}`
	newerTranscript := `{"text":"newest completed"}`
	olderCompletedAt := time.Now().Add(-2 * time.Hour)
	newerCompletedAt := time.Now().Add(-1 * time.Hour)
	olderRun := &models.TranscriptionJobExecution{
		TranscriptionJobID: job.ID,
		StartedAt:          olderCompletedAt.Add(-5 * time.Minute),
		CompletedAt:        &olderCompletedAt,
		ActualParameters:   models.WhisperXParams{ModelFamily: "whisper", Model: "base"},
		Status:             models.StatusCompleted,
		Transcript:         &olderTranscript,
		CreatedAt:          olderCompletedAt,
	}
	newerRun := &models.TranscriptionJobExecution{
		TranscriptionJobID: job.ID,
		StartedAt:          newerCompletedAt.Add(-5 * time.Minute),
		CompletedAt:        &newerCompletedAt,
		ActualParameters:   models.WhisperXParams{ModelFamily: "nvidia_canary"},
		Status:             models.StatusCompleted,
		Transcript:         &newerTranscript,
		CreatedAt:          newerCompletedAt,
	}
	olderRun.CalculateProcessingDuration()
	newerRun.CalculateProcessingDuration()
	assert.NoError(suite.T(), suite.helper.DB.Create(olderRun).Error)
	assert.NoError(suite.T(), suite.helper.DB.Create(newerRun).Error)

	w := suite.makeAuthenticatedRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/runs", nil, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var listResponse struct {
		ActiveRunID     string `json:"active_run_id"`
		PinnedRunID     string `json:"pinned_run_id"`
		ActiveRunPinned bool   `json:"active_run_pinned"`
	}
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &listResponse))
	assert.Equal(suite.T(), newerRun.ID, listResponse.ActiveRunID)
	assert.Empty(suite.T(), listResponse.PinnedRunID)
	assert.False(suite.T(), listResponse.ActiveRunPinned)

	w = suite.makeAuthenticatedRequest(http.MethodPost, "/api/v1/transcription/"+job.ID+"/runs/"+olderRun.ID+"/active", nil, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var pinnedJob models.TranscriptionJob
	assert.NoError(suite.T(), suite.helper.DB.First(&pinnedJob, "id = ?", job.ID).Error)
	assert.NotNil(suite.T(), pinnedJob.PinnedExecutionID)
	assert.Equal(suite.T(), olderRun.ID, *pinnedJob.PinnedExecutionID)

	w = suite.makeAuthenticatedRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/transcript", nil, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var transcriptResponse struct {
		RunID           string `json:"run_id"`
		ActiveRunPinned bool   `json:"active_run_pinned"`
		PinnedRunID     string `json:"pinned_run_id"`
		Available       bool   `json:"available"`
		Transcript      struct {
			Text string `json:"text"`
		} `json:"transcript"`
	}
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &transcriptResponse))
	assert.True(suite.T(), transcriptResponse.Available)
	assert.True(suite.T(), transcriptResponse.ActiveRunPinned)
	assert.Equal(suite.T(), olderRun.ID, transcriptResponse.RunID)
	assert.Equal(suite.T(), olderRun.ID, transcriptResponse.PinnedRunID)
	assert.Equal(suite.T(), "older but better", transcriptResponse.Transcript.Text)

	w = suite.makeAuthenticatedRequest(http.MethodDelete, "/api/v1/transcription/"+job.ID+"/runs/active", nil, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	w = suite.makeAuthenticatedRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/runs", nil, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &listResponse))
	assert.Equal(suite.T(), newerRun.ID, listResponse.ActiveRunID)
	assert.Empty(suite.T(), listResponse.PinnedRunID)
	assert.False(suite.T(), listResponse.ActiveRunPinned)
}

func (suite *APIHandlerTestSuite) TestSetActiveRunRejectsIncompleteRun() {
	job := suite.helper.CreateTestTranscriptionJob(suite.T(), "active run validation")
	startedAt := time.Now().Add(-5 * time.Minute)
	failedRun := &models.TranscriptionJobExecution{
		TranscriptionJobID: job.ID,
		StartedAt:          startedAt,
		ActualParameters:   models.WhisperXParams{ModelFamily: "whisper", Model: "base"},
		Status:             models.StatusFailed,
	}
	assert.NoError(suite.T(), suite.helper.DB.Create(failedRun).Error)

	w := suite.makeAuthenticatedRequest(http.MethodPost, "/api/v1/transcription/"+job.ID+"/runs/"+failedRun.ID+"/active", nil, true)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Contains(suite.T(), w.Body.String(), "Only completed runs can be active")
}
