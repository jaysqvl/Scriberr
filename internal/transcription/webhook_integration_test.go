package transcription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"scriberr/internal/models"
)

func TestWebhookIntegrationRejectsProtectedCallback(t *testing.T) {
	// Setup mock repository
	mockRepo := new(MockJobRepository)

	// Setup webhook server
	webhookCalled := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webhookCalled <- true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Setup service
	service := NewUnifiedTranscriptionService(mockRepo, "data/temp", "data/transcripts")

	// Setup test job
	callbackURL := server.URL
	jobID := "test-job-id"
	job := &models.TranscriptionJob{
		ID:        jobID,
		AudioPath: "/non/existent/file.wav", // This will cause processing to fail
		Status:    models.StatusPending,
		Parameters: models.WhisperXParams{
			CallbackURL: &callbackURL,
			ModelFamily: "whisper",
		},
	}

	// Mock expectations
	mockRepo.On("FindWithAssociations", mock.Anything, jobID).Return(job, nil)
	mockRepo.On("CreateExecution", mock.Anything, mock.Anything).Return(nil)
	mockRepo.On("UpdateExecution", mock.Anything, mock.Anything).Return(nil)

	// Execute
	// We expect an error because the file doesn't exist
	err := service.ProcessJob(context.Background(), jobID)
	assert.Error(t, err)

	// Loopback destinations must be rejected before any request is sent.
	select {
	case <-webhookCalled:
		t.Fatal("protected webhook destination was contacted")
	case <-time.After(100 * time.Millisecond):
	}

	mockRepo.AssertExpectations(t)
}
