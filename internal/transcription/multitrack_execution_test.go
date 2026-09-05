package transcription

import (
	"encoding/json"
	"testing"
	"time"

	"scriberr/internal/models"

	"github.com/stretchr/testify/require"
)

func TestApplyMultiTrackExecutionTimingUsesExistingExecution(t *testing.T) {
	assertions := require.New(t)
	startedAt := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	mergeStartedAt := startedAt.Add(3 * time.Second)
	mergeEndedAt := mergeStartedAt.Add(750 * time.Millisecond)
	completedAt := mergeEndedAt.Add(250 * time.Millisecond)
	execution := &models.TranscriptionJobExecution{
		ID:                 "execution-1",
		TranscriptionJobID: "job-1",
		StartedAt:          startedAt,
		Status:             models.StatusProcessing,
	}
	timings := []models.MultiTrackTiming{{
		TrackName: "host.wav",
		StartTime: startedAt,
		EndTime:   mergeStartedAt,
		Duration:  3000,
	}}

	err := applyMultiTrackExecutionTiming(
		execution,
		completedAt,
		4000,
		timings,
		mergeStartedAt,
		mergeEndedAt,
		750,
	)

	assertions.NoError(err)
	assertions.Equal("execution-1", execution.ID)
	assertions.Equal(completedAt, *execution.CompletedAt)
	assertions.Equal(int64(4000), *execution.ProcessingDuration)
	assertions.Equal(mergeStartedAt, *execution.MergeStartTime)
	assertions.Equal(mergeEndedAt, *execution.MergeEndTime)
	assertions.Equal(int64(750), *execution.MergeDuration)

	var savedTimings []models.MultiTrackTiming
	assertions.NoError(json.Unmarshal([]byte(*execution.MultiTrackTimings), &savedTimings))
	assertions.Equal(timings, savedTimings)
}

func TestApplyMultiTrackExecutionTimingRequiresExecution(t *testing.T) {
	err := applyMultiTrackExecutionTiming(nil, time.Now(), 0, nil, time.Now(), time.Now(), 0)
	require.ErrorContains(t, err, "execution record is required")
}

func TestMultiTrackTranscriberRegistryIsJobScoped(t *testing.T) {
	assertions := require.New(t)
	service := NewUnifiedTranscriptionService(nil, "data/temp", "data/transcripts")
	first := &MultiTrackTranscriber{}
	second := &MultiTrackTranscriber{}

	assertions.NoError(service.registerMultiTrackTranscriber("job-1", first))
	assertions.NoError(service.registerMultiTrackTranscriber("job-2", second))
	assertions.ErrorContains(service.registerMultiTrackTranscriber("job-1", second), "already active")

	service.unregisterMultiTrackTranscriber("job-1", second)
	service.multiTrackMutex.RLock()
	assertions.Same(first, service.multiTrackTranscribers["job-1"])
	assertions.Same(second, service.multiTrackTranscribers["job-2"])
	service.multiTrackMutex.RUnlock()

	service.unregisterMultiTrackTranscriber("job-1", first)
	service.multiTrackMutex.RLock()
	_, firstExists := service.multiTrackTranscribers["job-1"]
	_, secondExists := service.multiTrackTranscribers["job-2"]
	service.multiTrackMutex.RUnlock()
	assertions.False(firstExists)
	assertions.True(secondExists)
}
