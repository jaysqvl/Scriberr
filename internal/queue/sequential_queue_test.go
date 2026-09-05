package queue

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"scriberr/internal/models"
	"scriberr/internal/repository"
	"scriberr/pkg/logger"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type sequentialRecordingProcessor struct {
	repo       repository.JobRepository
	blockModel string
	errorModel string
	started    chan string
	mu         sync.Mutex
	models     []string
	active     int32
	maxActive  int32
}

type blockingMultiTrackStopProcessor struct {
	repo             repository.JobRepository
	started          chan string
	terminateStarted chan struct{}
	releaseTerminate chan struct{}
	terminateOnce    sync.Once
}

type prematureParentCompletionProcessor struct {
	repo           repository.JobRepository
	started        chan string
	markedComplete chan struct{}
	release        chan struct{}
	markedOnce     sync.Once
}

type cancellationAuditProcessor struct {
	repo           repository.JobRepository
	started        chan string
	executionID    chan string
	cancelObserved chan struct{}
	releaseCleanup chan struct{}
	cancelOnce     sync.Once
}

func (p *cancellationAuditProcessor) ProcessJob(ctx context.Context, jobID string) error {
	return p.ProcessJobWithProcess(ctx, jobID, func(*exec.Cmd) {})
}

func (p *cancellationAuditProcessor) ProcessJobWithProcess(ctx context.Context, jobID string, _ func(*exec.Cmd)) error {
	job, err := p.repo.FindByID(context.Background(), jobID)
	if err != nil {
		return err
	}
	p.started <- job.Parameters.Model
	if job.Parameters.Model != "audited-stop" {
		return nil
	}

	execution := &models.TranscriptionJobExecution{
		TranscriptionJobID: jobID,
		StartedAt:          time.Now(),
		Status:             models.StatusProcessing,
	}
	if err := p.repo.CreateExecution(context.Background(), execution); err != nil {
		return err
	}
	p.executionID <- execution.ID
	<-ctx.Done()
	p.cancelOnce.Do(func() { close(p.cancelObserved) })
	<-p.releaseCleanup
	completedAt := time.Now()
	reason := "cancelled during audit test"
	execution.Status = models.StatusFailed
	execution.ErrorMessage = &reason
	execution.CompletedAt = &completedAt
	execution.CalculateProcessingDuration()
	if err := p.repo.UpdateExecution(context.Background(), execution); err != nil {
		return err
	}
	return ctx.Err()
}

func (p *prematureParentCompletionProcessor) ProcessJob(ctx context.Context, jobID string) error {
	return p.ProcessJobWithProcess(ctx, jobID, func(*exec.Cmd) {})
}

func (p *prematureParentCompletionProcessor) ProcessJobWithProcess(_ context.Context, jobID string, _ func(*exec.Cmd)) error {
	job, err := p.repo.FindByID(context.Background(), jobID)
	if err != nil {
		return err
	}
	p.started <- job.Parameters.Model
	if job.Parameters.Model == "premature-parent-complete" {
		if err := p.repo.UpdateStatus(context.Background(), jobID, models.StatusCompleted); err != nil {
			return err
		}
		p.markedOnce.Do(func() { close(p.markedComplete) })
		<-p.release
	}
	return nil
}

func (p *blockingMultiTrackStopProcessor) ProcessJob(ctx context.Context, jobID string) error {
	return p.ProcessJobWithProcess(ctx, jobID, func(*exec.Cmd) {})
}

func (p *blockingMultiTrackStopProcessor) ProcessJobWithProcess(ctx context.Context, jobID string, _ func(*exec.Cmd)) error {
	job, err := p.repo.FindByID(context.Background(), jobID)
	if err != nil {
		return err
	}
	p.started <- job.Parameters.Model
	if job.Parameters.Model == "stopped-multitrack" {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (p *blockingMultiTrackStopProcessor) TerminateMultiTrackJob(string) error {
	p.terminateOnce.Do(func() { close(p.terminateStarted) })
	<-p.releaseTerminate
	return nil
}

func (p *blockingMultiTrackStopProcessor) IsMultiTrackJob(string) bool { return true }

func (p *sequentialRecordingProcessor) ProcessJob(ctx context.Context, jobID string) error {
	return p.ProcessJobWithProcess(ctx, jobID, func(*exec.Cmd) {})
}

func (p *sequentialRecordingProcessor) ProcessJobWithProcess(ctx context.Context, jobID string, _ func(*exec.Cmd)) error {
	job, err := p.repo.FindByID(context.Background(), jobID)
	if err != nil {
		return err
	}
	current := atomic.AddInt32(&p.active, 1)
	defer atomic.AddInt32(&p.active, -1)
	for {
		maximum := atomic.LoadInt32(&p.maxActive)
		if current <= maximum || atomic.CompareAndSwapInt32(&p.maxActive, maximum, current) {
			break
		}
	}

	p.mu.Lock()
	p.models = append(p.models, job.Parameters.Model)
	p.mu.Unlock()
	p.started <- job.Parameters.Model
	if job.Parameters.Model == p.errorModel {
		return context.Canceled // processor error path; context itself remains live
	}
	if job.Parameters.Model == p.blockModel {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func TestFailedSequentialRunAdvancesToNext(t *testing.T) {
	tq, _, runRepo, _, jobID, processor := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, errorModel: "fails", started: make(chan string, 2)}
	})
	tq.Start()
	defer tq.Stop()
	_, err := tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{Model: "fails"}},
		{Parameters: models.WhisperXParams{Model: "after-failure"}},
	})
	require.NoError(t, err)
	require.Equal(t, "fails", <-processor.started)
	require.Equal(t, "after-failure", <-processor.started)
	require.Eventually(t, func() bool {
		items, listErr := runRepo.List(context.Background(), jobID, true)
		if listErr != nil || len(items) != 2 {
			return false
		}
		statuses := make(map[string]models.TranscriptionQueueStatus)
		for _, item := range items {
			statuses[item.Parameters.Model] = item.Status
		}
		return statuses["fails"] == models.QueueStatusFailed && statuses["after-failure"] == models.QueueStatusCompleted
	}, 3*time.Second, 10*time.Millisecond)
}

func TestTimedOutSequentialRunAdvancesToNext(t *testing.T) {
	tq, _, runRepo, _, jobID, processor := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, blockModel: "times-out", started: make(chan string, 2)}
	})
	tq.SetJobTimeout(25 * time.Millisecond)
	tq.Start()
	defer tq.Stop()
	_, err := tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{Model: "times-out"}},
		{Parameters: models.WhisperXParams{Model: "after-timeout"}},
	})
	require.NoError(t, err)
	require.Equal(t, "times-out", <-processor.started)
	require.Equal(t, "after-timeout", <-processor.started)
	require.Eventually(t, func() bool {
		items, listErr := runRepo.List(context.Background(), jobID, true)
		if listErr != nil || len(items) != 2 {
			return false
		}
		statuses := make(map[string]models.TranscriptionQueueStatus)
		for _, item := range items {
			statuses[item.Parameters.Model] = item.Status
		}
		return statuses["times-out"] == models.QueueStatusFailed && statuses["after-timeout"] == models.QueueStatusCompleted
	}, 3*time.Second, 10*time.Millisecond)
}

func (p *sequentialRecordingProcessor) processedModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.models...)
}

func newSequentialQueueTest(t *testing.T, status models.JobStatus, processorFactory func(repository.JobRepository) JobProcessor) (*TaskQueue, repository.JobRepository, repository.TranscriptionQueueRepository, *gorm.DB, string, *sequentialRecordingProcessor) {
	t.Helper()
	logger.Init("error")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&models.TranscriptionJob{}, &models.TranscriptionJobExecution{}, &models.TranscriptionQueueItem{}))
	job := &models.TranscriptionJob{ID: "sequential-audio", AudioPath: "audio.wav", Status: status}
	require.NoError(t, db.Create(job).Error)
	jobRepo := repository.NewJobRepository(db)
	runRepo := repository.NewTranscriptionQueueRepository(db)
	processor := processorFactory(jobRepo)
	tq := NewTaskQueue(1, processor, jobRepo)
	tq.SetTranscriptionQueueRepository(runRepo)
	recording, _ := processor.(*sequentialRecordingProcessor)
	return tq, jobRepo, runRepo, db, job.ID, recording
}

func TestSequentialRunsExecuteInOrderWithoutOverlap(t *testing.T) {
	tq, _, runRepo, _, jobID, processor := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, started: make(chan string, 3)}
	})
	tq.Start()
	defer tq.Stop()

	_, err := tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{Model: "tiny"}},
		{Parameters: models.WhisperXParams{Model: "small"}},
		{Parameters: models.WhisperXParams{Model: "medium"}},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		items, listErr := runRepo.List(context.Background(), jobID, true)
		if listErr != nil || len(items) != 3 {
			return false
		}
		for _, item := range items {
			if item.Status != models.QueueStatusCompleted {
				return false
			}
		}
		return true
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{"tiny", "small", "medium"}, processor.processedModels())
	require.Equal(t, int32(1), atomic.LoadInt32(&processor.maxActive))
}

func TestStoppingSequentialRunAdvancesAfterProcessorCleanup(t *testing.T) {
	tq, _, runRepo, _, jobID, processor := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, blockModel: "slow", started: make(chan string, 2)}
	})
	tq.Start()
	defer tq.Stop()

	queued, err := tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{Model: "slow"}},
		{Parameters: models.WhisperXParams{Model: "next"}},
	})
	require.NoError(t, err)
	require.Equal(t, "slow", <-processor.started)
	var slowID, nextID string
	for _, item := range queued {
		switch item.Parameters.Model {
		case "slow":
			slowID = item.ID
		case "next":
			nextID = item.ID
		}
	}
	require.ErrorIs(t, tq.KillJobIfCurrent(jobID, nextID), ErrQueueTargetChanged)
	require.ErrorIs(t, tq.KillJobIfCurrent(jobID, ""), ErrQueueTargetChanged)
	require.True(t, tq.IsJobRunning(jobID))
	require.NoError(t, tq.KillJobIfCurrent(jobID, slowID))
	require.Equal(t, "next", <-processor.started)

	require.Eventually(t, func() bool {
		items, listErr := runRepo.List(context.Background(), jobID, true)
		if listErr != nil || len(items) != 2 {
			return false
		}
		statuses := map[string]models.TranscriptionQueueStatus{}
		for _, item := range items {
			statuses[item.Parameters.Model] = item.Status
		}
		return statuses["slow"] == models.QueueStatusCancelled && statuses["next"] == models.QueueStatusCompleted
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, int32(1), atomic.LoadInt32(&processor.maxActive))
}

func TestMultiTrackStopFinishesTargetTerminationBeforePromotingSuccessor(t *testing.T) {
	var processor *blockingMultiTrackStopProcessor
	tq, _, runRepo, _, jobID, _ := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		processor = &blockingMultiTrackStopProcessor{
			repo:             repo,
			started:          make(chan string, 2),
			terminateStarted: make(chan struct{}),
			releaseTerminate: make(chan struct{}),
		}
		return processor
	})
	tq.Start()
	defer tq.Stop()

	queued, err := tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{Model: "stopped-multitrack"}},
		{Parameters: models.WhisperXParams{Model: "successor-multitrack"}},
	})
	require.NoError(t, err)
	require.Equal(t, "stopped-multitrack", <-processor.started)
	var stoppedID string
	for _, item := range queued {
		if item.Parameters.Model == "stopped-multitrack" {
			stoppedID = item.ID
		}
	}
	require.NotEmpty(t, stoppedID)

	killDone := make(chan error, 1)
	go func() { killDone <- tq.KillJobIfCurrent(jobID, stoppedID) }()
	select {
	case <-processor.terminateStarted:
	case <-time.After(time.Second):
		t.Fatal("multi-track termination was not invoked")
	}
	select {
	case model := <-processor.started:
		t.Fatalf("successor %q started before target-specific termination finished", model)
	case <-time.After(100 * time.Millisecond):
	}
	close(processor.releaseTerminate)
	require.NoError(t, <-killDone)
	require.Equal(t, "successor-multitrack", <-processor.started)

	require.Eventually(t, func() bool {
		items, listErr := runRepo.List(context.Background(), jobID, true)
		if listErr != nil || len(items) != 2 {
			return false
		}
		return items[0].Status == models.QueueStatusCancelled && items[1].Status == models.QueueStatusCompleted
	}, 3*time.Second, 10*time.Millisecond)
}

func TestAddingRunWaitsForExistingOwnerAfterParentLooksCompleted(t *testing.T) {
	var processor *prematureParentCompletionProcessor
	tq, jobRepo, runRepo, _, jobID, _ := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		processor = &prematureParentCompletionProcessor{
			repo:           repo,
			started:        make(chan string, 2),
			markedComplete: make(chan struct{}),
			release:        make(chan struct{}),
		}
		return processor
	})
	tq.Start()
	defer tq.Stop()

	job, err := jobRepo.FindByID(context.Background(), jobID)
	require.NoError(t, err)
	job.Parameters = models.WhisperXParams{Model: "premature-parent-complete"}
	require.NoError(t, tq.StartImmediateRun(context.Background(), job))
	require.Equal(t, "premature-parent-complete", <-processor.started)
	select {
	case <-processor.markedComplete:
	case <-time.After(time.Second):
		t.Fatal("processor did not publish its intentionally premature parent status")
	}

	items, err := tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{{
		Parameters: models.WhisperXParams{Model: "after-cleanup"},
	}})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, models.QueueStatusQueued, items[0].Status)
	select {
	case model := <-processor.started:
		t.Fatalf("successor %q started before the existing processor returned", model)
	case <-time.After(100 * time.Millisecond):
	}

	close(processor.release)
	require.Equal(t, "after-cleanup", <-processor.started)
	require.Eventually(t, func() bool {
		finished, findErr := runRepo.FindByID(context.Background(), jobID, items[0].ID)
		return findErr == nil && finished.Status == models.QueueStatusCompleted
	}, 3*time.Second, 10*time.Millisecond)
}

func TestStoppedRunLinksExecutionOnlyAfterProcessorCleanup(t *testing.T) {
	var processor *cancellationAuditProcessor
	tq, _, runRepo, db, jobID, _ := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		processor = &cancellationAuditProcessor{
			repo:           repo,
			started:        make(chan string, 2),
			executionID:    make(chan string, 1),
			cancelObserved: make(chan struct{}),
			releaseCleanup: make(chan struct{}),
		}
		return processor
	})
	tq.Start()
	defer tq.Stop()

	queued, err := tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{Model: "audited-stop"}},
		{Parameters: models.WhisperXParams{Model: "after-audited-stop"}},
	})
	require.NoError(t, err)
	require.Equal(t, "audited-stop", <-processor.started)
	executionID := <-processor.executionID
	var stoppedID string
	for _, item := range queued {
		if item.Parameters.Model == "audited-stop" {
			stoppedID = item.ID
		}
	}
	require.NotEmpty(t, stoppedID)
	require.NoError(t, tq.KillJobIfCurrent(jobID, stoppedID))
	select {
	case <-processor.cancelObserved:
	case <-time.After(time.Second):
		t.Fatal("processor did not observe cancellation")
	}

	active, err := runRepo.FindByID(context.Background(), jobID, stoppedID)
	require.NoError(t, err)
	require.Equal(t, models.QueueStatusProcessing, active.Status)
	require.Nil(t, active.CompletedAt)
	require.Nil(t, active.ExecutionID)

	cleanupReleasedAt := time.Now()
	close(processor.releaseCleanup)
	require.Equal(t, "after-audited-stop", <-processor.started)
	require.Eventually(t, func() bool {
		finished, findErr := runRepo.FindByID(context.Background(), jobID, stoppedID)
		return findErr == nil && finished.Status == models.QueueStatusCancelled &&
			finished.ExecutionID != nil && *finished.ExecutionID == executionID &&
			finished.CompletedAt != nil && !finished.CompletedAt.Before(cleanupReleasedAt)
	}, 3*time.Second, 10*time.Millisecond)

	var execution models.TranscriptionJobExecution
	require.NoError(t, db.First(&execution, "id = ?", executionID).Error)
	require.Equal(t, models.StatusFailed, execution.Status)
	require.NotNil(t, execution.CompletedAt)
}

func TestStartupRecoversExactPendingQueueItem(t *testing.T) {
	tq, _, runRepo, _, jobID, processor := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, started: make(chan string, 1)}
	})
	require.NoError(t, runRepo.Append(context.Background(), jobID, []models.TranscriptionQueueItem{{
		Parameters: models.WhisperXParams{Model: "recovered"},
	}}))
	pending, err := runRepo.PromoteNext(context.Background(), jobID)
	require.NoError(t, err)
	require.NotNil(t, pending)

	tq.Start()
	defer tq.Stop()
	require.Equal(t, "recovered", <-processor.started)
	require.Eventually(t, func() bool {
		item, findErr := runRepo.FindByID(context.Background(), jobID, pending.ID)
		return findErr == nil && item.Status == models.QueueStatusCompleted
	}, 3*time.Second, 10*time.Millisecond)
}

func TestStartupFailsInterruptedRunAndExecutionBeforeAdvancing(t *testing.T) {
	tq, _, runRepo, db, jobID, processor := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, started: make(chan string, 1)}
	})
	apiKey := "temporary-secret"
	require.NoError(t, runRepo.Append(context.Background(), jobID, []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{Model: "interrupted", APIKey: &apiKey}},
		{Parameters: models.WhisperXParams{Model: "recovered-next"}},
	}))
	active, err := runRepo.PromoteNext(context.Background(), jobID)
	require.NoError(t, err)
	_, claimed, err := runRepo.ClaimPending(context.Background(), jobID, active.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	startedAt := time.Now().Add(-time.Minute)
	execution := &models.TranscriptionJobExecution{
		TranscriptionJobID: jobID,
		StartedAt:          startedAt,
		Status:             models.StatusProcessing,
	}
	require.NoError(t, db.Create(execution).Error)

	tq.Start()
	defer tq.Stop()
	require.Equal(t, "recovered-next", <-processor.started)
	require.Eventually(t, func() bool {
		var recovered models.TranscriptionJobExecution
		if findErr := db.First(&recovered, "id = ?", execution.ID).Error; findErr != nil {
			return false
		}
		return recovered.Status == models.StatusFailed && recovered.CompletedAt != nil && recovered.ErrorMessage != nil
	}, 3*time.Second, 10*time.Millisecond)
	interrupted, err := runRepo.FindByID(context.Background(), jobID, active.ID)
	require.NoError(t, err)
	require.Equal(t, models.QueueStatusFailed, interrupted.Status)
	require.Nil(t, interrupted.Parameters.APIKey)
}

func TestGracefulShutdownLeavesSuccessorPendingForRecovery(t *testing.T) {
	tq, _, runRepo, _, jobID, processor := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, blockModel: "shutdown-active", started: make(chan string, 2)}
	})
	tq.Start()
	_, err := tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{Model: "shutdown-active"}},
		{Parameters: models.WhisperXParams{Model: "after-restart"}},
	})
	require.NoError(t, err)
	require.Equal(t, "shutdown-active", <-processor.started)

	stopped := make(chan struct{})
	go func() {
		tq.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("task queue did not stop")
	}

	items, err := runRepo.List(context.Background(), jobID, true)
	require.NoError(t, err)
	require.Len(t, items, 2)
	statuses := make(map[string]models.TranscriptionQueueStatus)
	errorsByModel := make(map[string]string)
	for _, item := range items {
		statuses[item.Parameters.Model] = item.Status
		if item.ErrorMessage != nil {
			errorsByModel[item.Parameters.Model] = *item.ErrorMessage
		}
	}
	require.Equal(t, models.QueueStatusFailed, statuses["shutdown-active"])
	require.Equal(t, "Job interrupted by server shutdown", errorsByModel["shutdown-active"])
	require.Equal(t, models.QueueStatusPending, statuses["after-restart"])
	select {
	case model := <-processor.started:
		t.Fatalf("successor %q started during shutdown", model)
	default:
	}
}

func TestSequentialDispatchDoesNotBlockWhenChannelIsFull(t *testing.T) {
	tq, _, _, _, jobID, _ := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, started: make(chan string, 1)}
	})
	tq.jobChannel = make(chan queuedTask, 1)
	tq.jobChannel <- queuedTask{JobID: "occupies-capacity"}

	done := make(chan error, 1)
	go func() {
		_, err := tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{{
			Parameters: models.WhisperXParams{Model: "does-not-block"},
		}})
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("persisting a sequential run blocked on in-memory channel capacity")
	}
	tq.cancel()
}

func TestReconcilerDeduplicatesPendingDispatches(t *testing.T) {
	tq, _, runRepo, _, jobID, _ := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, started: make(chan string, 1)}
	})
	tq.jobChannel = make(chan queuedTask, 10)
	require.NoError(t, runRepo.Append(context.Background(), jobID, []models.TranscriptionQueueItem{{
		Parameters: models.WhisperXParams{Model: "deduplicated"},
	}}))
	pending, err := runRepo.PromoteNext(context.Background(), jobID)
	require.NoError(t, err)
	require.NotNil(t, pending)

	for range 4 {
		tq.reconcileSequentialRuns()
	}
	require.Len(t, tq.jobChannel, 1)
	tq.dispatchMutex.Lock()
	require.Len(t, tq.scheduledTasks, 1)
	tq.dispatchMutex.Unlock()
	tq.cancel()
}

func TestStartupRecoveryDoesNotBlockWhenPendingJobsExceedChannelCapacity(t *testing.T) {
	logger.Init("error")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&models.TranscriptionJob{}, &models.TranscriptionJobExecution{}, &models.TranscriptionQueueItem{}))
	for index := range 5 {
		job := &models.TranscriptionJob{
			ID:        fmt.Sprintf("startup-pending-%d", index),
			AudioPath: fmt.Sprintf("audio-%d.wav", index),
			Status:    models.StatusPending,
			Parameters: models.WhisperXParams{
				ModelFamily: "whisper",
				Model:       "blocked",
			},
		}
		require.NoError(t, db.Create(job).Error)
	}

	jobRepo := repository.NewJobRepository(db)
	processor := &sequentialRecordingProcessor{
		repo:       jobRepo,
		blockModel: "blocked",
		errorModel: "never-error",
		started:    make(chan string, 5),
	}
	tq := NewTaskQueue(1, processor, jobRepo)
	tq.SetTranscriptionQueueRepository(repository.NewTranscriptionQueueRepository(db))
	tq.jobChannel = make(chan queuedTask, 1)
	tq.reconcileEvery = time.Hour

	startedQueue := make(chan struct{})
	go func() {
		tq.Start()
		close(startedQueue)
	}()
	select {
	case <-startedQueue:
	case <-time.After(500 * time.Millisecond):
		tq.cancel()
		select {
		case <-startedQueue:
		case <-time.After(time.Second):
			t.Fatal("task queue recovery did not stop after cancellation")
		}
		tq.wg.Wait()
		t.Fatal("task queue startup blocked behind recovered pending work")
	}
	defer tq.Stop()

	select {
	case <-processor.started:
	case <-time.After(time.Second):
		t.Fatal("no recovered pending job began processing")
	}
	pending, err := jobRepo.FindByStatus(context.Background(), models.StatusPending)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(pending), 3)
}

func TestReconcilerRepairsOrphanedParentBeforePromotingNext(t *testing.T) {
	tq, jobRepo, runRepo, _, jobID, _ := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, started: make(chan string, 1)}
	})
	require.NoError(t, runRepo.Append(context.Background(), jobID, []models.TranscriptionQueueItem{
		{Parameters: models.WhisperXParams{Model: "finished-item"}},
		{Parameters: models.WhisperXParams{Model: "waiting-successor"}},
	}))
	active, err := runRepo.PromoteNext(context.Background(), jobID)
	require.NoError(t, err)
	require.NotNil(t, active)
	_, claimed, err := runRepo.ClaimPending(context.Background(), jobID, active.ID)
	require.NoError(t, err)
	require.True(t, claimed)

	// Reproduce a partial terminal write: the durable item committed, while
	// the independently persisted parent remained processing.
	require.NoError(t, runRepo.Finalize(context.Background(), jobID, active.ID, models.QueueStatusCompleted, "", nil))
	tq.reconcileSequentialRuns()

	job, err := jobRepo.FindByID(context.Background(), jobID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, job.Status)
	pending, err := runRepo.FindPending(context.Background(), jobID)
	require.NoError(t, err)
	require.Equal(t, "waiting-successor", pending.Parameters.Model)
	require.Len(t, tq.jobChannel, 1)
	tq.cancel()
}

func TestDeletionReservationBlocksNewRuns(t *testing.T) {
	tq, jobRepo, _, _, jobID, _ := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, started: make(chan string, 1)}
	})
	_, release, err := tq.ReserveJobDeletion(context.Background(), jobID)
	require.NoError(t, err)
	require.NotNil(t, release)

	_, err = tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{{
		Parameters: models.WhisperXParams{Model: "too-late"},
	}})
	require.ErrorIs(t, err, ErrJobStateChanged)
	job, err := jobRepo.FindByID(context.Background(), jobID)
	require.NoError(t, err)
	job.Parameters = models.WhisperXParams{Model: "also-too-late"}
	require.ErrorIs(t, tq.StartImmediateRun(context.Background(), job), ErrJobStateChanged)

	release()
	_, err = tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{{
		Parameters: models.WhisperXParams{Model: "accepted-after-release"},
	}})
	require.NoError(t, err)
	tq.cancel()
}

func TestImmediateAndSequentialAdmissionCannotOverwriteEachOther(t *testing.T) {
	tq, jobRepo, runRepo, _, jobID, _ := newSequentialQueueTest(t, models.StatusCompleted, func(repo repository.JobRepository) JobProcessor {
		return &sequentialRecordingProcessor{repo: repo, started: make(chan string, 1)}
	})
	updatedJob, err := jobRepo.FindByID(context.Background(), jobID)
	require.NoError(t, err)
	updatedJob.Parameters = models.WhisperXParams{Model: "immediate"}
	updatedJob.Transcript = nil
	updatedJob.ErrorMessage = nil

	tq.jobsMutex.Lock()
	addResult := make(chan error, 1)
	startResult := make(chan error, 1)
	go func() {
		_, addErr := tq.AddSequentialRuns(context.Background(), jobID, []models.TranscriptionQueueItem{{
			Parameters: models.WhisperXParams{Model: "sequential"},
		}})
		addResult <- addErr
	}()
	go func() {
		startResult <- tq.StartImmediateRun(context.Background(), updatedJob)
	}()
	tq.jobsMutex.Unlock()

	require.NoError(t, <-addResult)
	startErr := <-startResult
	if startErr != nil {
		require.ErrorIs(t, startErr, ErrJobStateChanged)
	}

	job, err := jobRepo.FindByID(context.Background(), jobID)
	require.NoError(t, err)
	items, err := runRepo.List(context.Background(), jobID, false)
	require.NoError(t, err)
	require.Len(t, items, 1)
	if startErr == nil {
		require.Equal(t, "immediate", job.Parameters.Model)
		require.Equal(t, models.QueueStatusQueued, items[0].Status)
	} else {
		require.Equal(t, "sequential", job.Parameters.Model)
		require.Equal(t, models.QueueStatusPending, items[0].Status)
	}
	tq.cancel()
}
