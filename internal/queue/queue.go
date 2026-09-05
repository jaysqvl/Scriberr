package queue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"scriberr/internal/models"
	"scriberr/internal/repository"
	"scriberr/pkg/logger"

	"gorm.io/gorm"
)

// RunningJob tracks both context cancellation and OS process
type RunningJob struct {
	Cancel      context.CancelFunc
	Process     *exec.Cmd
	QueueItemID string
	Finishing   bool
}

// queuedTask carries the durable queue-item identity for sequential runs.
// Legacy callers intentionally leave QueueItemID empty.
type queuedTask struct {
	JobID       string
	QueueItemID string
}

var (
	ErrQueueTargetChanged = errors.New("the active run changed before cancellation")
	ErrJobStateChanged    = errors.New("the transcription job is no longer idle")
	errQueueFull          = errors.New("queue is full")
)

// TaskQueue manages transcription job processing
type TaskQueue struct {
	minWorkers     int
	maxWorkers     int
	currentWorkers int64 // Use atomic for thread-safe access
	jobChannel     chan queuedTask
	ctx            context.Context
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	processor      JobProcessor
	runningJobs    map[string]*RunningJob
	deletingJobs   map[string]struct{}
	jobsMutex      sync.RWMutex
	dispatchMutex  sync.Mutex
	scheduledTasks map[queuedTask]struct{}
	autoScale      bool
	lastScaleTime  time.Time
	jobRepo        repository.JobRepository
	runQueueRepo   repository.TranscriptionQueueRepository
	jobTimeout     time.Duration
	reconcileEvery time.Duration
}

// JobProcessor defines the interface for processing jobs
type JobProcessor interface {
	ProcessJob(ctx context.Context, jobID string) error
	ProcessJobWithProcess(ctx context.Context, jobID string, registerProcess func(*exec.Cmd)) error
}

// MultiTrackJobProcessor extends JobProcessor with multi-track specific methods
type MultiTrackJobProcessor interface {
	JobProcessor
	TerminateMultiTrackJob(jobID string) error
	IsMultiTrackJob(jobID string) bool
}

// getOptimalWorkerCount calculates optimal worker count based on system resources
func getOptimalWorkerCount() (min, max int) {
	numCPU := runtime.NumCPU()

	// Check for environment variable override
	if workerStr := os.Getenv("QUEUE_WORKERS"); workerStr != "" {
		if workers, err := strconv.Atoi(workerStr); err == nil && workers > 0 {
			return workers, workers // Fixed worker count
		}
	}

	// For transcription workloads, we typically want fewer workers than CPUs
	// since each job is CPU and I/O intensive
	if numCPU <= 2 {
		return 1, 2
	}
	if numCPU <= 4 {
		return 1, 3
	}
	if numCPU <= 8 {
		return 2, 4
	}
	return 2, 6 // Cap at 6 for very high CPU systems
}

// NewTaskQueue creates a new task queue with auto-scaling capabilities
func NewTaskQueue(legacyWorkers int, processor JobProcessor, jobRepo repository.JobRepository) *TaskQueue {
	ctx, cancel := context.WithCancel(context.Background())

	// Calculate optimal worker counts, fallback to legacy parameter
	min, max := getOptimalWorkerCount()
	// Only use legacy parameter as fallback when QUEUE_WORKERS env var is not set
	// TODO: Deprecate `legacyWorkers` and rely on `getOptimalWorkerCount` instead.
	if os.Getenv("QUEUE_WORKERS") == "" && legacyWorkers > 0 {
		min = legacyWorkers
		max = legacyWorkers
	}

	// Check if auto-scaling should be enabled
	autoScale := os.Getenv("QUEUE_AUTO_SCALE") != "false"
	if min == max {
		autoScale = false // Disable auto-scaling if min == max
	}

	return &TaskQueue{
		minWorkers:     min,
		maxWorkers:     max,
		currentWorkers: int64(min),
		jobChannel:     make(chan queuedTask, 200), // Increased buffer for better throughput
		ctx:            ctx,
		cancel:         cancel,
		processor:      processor,
		runningJobs:    make(map[string]*RunningJob),
		deletingJobs:   make(map[string]struct{}),
		scheduledTasks: make(map[queuedTask]struct{}),
		autoScale:      autoScale,
		lastScaleTime:  time.Now(),
		jobRepo:        jobRepo,
		jobTimeout:     2 * time.Hour,
		reconcileEvery: 5 * time.Second,
	}
}

// SetTranscriptionQueueRepository enables durable per-audio sequential runs.
// It remains optional so embedders and legacy tests keep their existing queue
// behavior until they explicitly provide the repository.
func (tq *TaskQueue) SetTranscriptionQueueRepository(repo repository.TranscriptionQueueRepository) {
	tq.runQueueRepo = repo
}

// SetJobTimeout bounds each queued transcription and its child processes.
func (tq *TaskQueue) SetJobTimeout(timeout time.Duration) {
	if timeout > 0 {
		tq.jobTimeout = timeout
	}
}

// Start starts the task queue workers
func (tq *TaskQueue) Start() {
	workers := int(atomic.LoadInt64(&tq.currentWorkers))
	logger.Debug("Starting task queue",
		"workers", workers,
		"min_workers", tq.minWorkers,
		"max_workers", tq.maxWorkers,
		"auto_scale", tq.autoScale)

	// Reset any zombie jobs from previous runs synchronously before starting workers
	tq.ResetZombieJobs()

	// Repair interrupted durable queue items and promote the next waiting run
	// before recovering pending work.
	tq.recoverSequentialRuns()

	// Start initial workers
	for i := 0; i < workers; i++ {
		tq.wg.Add(1)
		go tq.worker(i)
	}

	// One-time recovery: enqueue any pending jobs left from previous server run.
	// Workers are started first so recovery cannot deadlock when more than the
	// channel capacity was persisted.
	tq.recoverPendingJobs()

	// Start auto-scaling monitor if enabled
	if tq.autoScale {
		tq.wg.Add(1)
		go tq.autoScaler()
	}
	if tq.runQueueRepo != nil {
		tq.wg.Add(1)
		go tq.sequentialReconciler()
	}
}

// Stop stops the task queue
func (tq *TaskQueue) Stop() {
	logger.Debug("Stopping task queue")
	tq.cancel()
	// Do not close jobChannel here as it causes panics in EnqueueJob
	// The channel will be garbage collected when the queue is no longer referenced
	tq.wg.Wait()
	logger.Debug("Task queue stopped")
}

// EnqueueJob adds a job to the queue
func (tq *TaskQueue) EnqueueJob(jobID string) error {
	return tq.enqueueTask(queuedTask{JobID: jobID}, false)
}

func (tq *TaskQueue) enqueueSequentialJob(jobID, queueItemID string) error {
	task := queuedTask{JobID: jobID, QueueItemID: queueItemID}
	if err := tq.enqueueTask(task, false); err != nil {
		if !errors.Is(err, errQueueFull) {
			return err
		}
		// The durable item is already pending. The reconciliation loop retries
		// dispatch without holding an API request or a worker on channel capacity.
		logger.Debug("Sequential run persisted for reconciled dispatch", "job_id", jobID, "queue_item_id", queueItemID)
	}
	return nil
}

func (tq *TaskQueue) enqueueTask(task queuedTask, waitForCapacity bool) error {
	// Check if queue is already shut down
	select {
	case <-tq.ctx.Done():
		return fmt.Errorf("queue is shutting down")
	default:
	}

	// A durable pending item may be discovered by startup recovery, the
	// reconciler, and event-driven promotion at nearly the same time. Keep one
	// in-memory dispatch reservation per exact identity until a worker has
	// claimed (or rejected) it, so reconciliation cannot amplify duplicate
	// channel entries while workers are busy.
	tq.dispatchMutex.Lock()
	if _, scheduled := tq.scheduledTasks[task]; scheduled {
		tq.dispatchMutex.Unlock()
		return nil
	}
	tq.scheduledTasks[task] = struct{}{}
	tq.dispatchMutex.Unlock()

	releaseOnFailure := true
	defer func() {
		if releaseOnFailure {
			tq.releaseScheduledTask(task)
		}
	}()

	if waitForCapacity {
		select {
		case tq.jobChannel <- task:
			releaseOnFailure = false
			return nil
		case <-tq.ctx.Done():
			return fmt.Errorf("queue is shutting down")
		}
	}

	select {
	case tq.jobChannel <- task:
		releaseOnFailure = false
		return nil
	case <-tq.ctx.Done():
		return fmt.Errorf("queue is shutting down")
	default:
		return errQueueFull
	}
}

func (tq *TaskQueue) releaseScheduledTask(task queuedTask) {
	tq.dispatchMutex.Lock()
	delete(tq.scheduledTasks, task)
	tq.dispatchMutex.Unlock()
}

// AddSequentialRuns durably appends one or more immutable run requests and, if
// the audio is idle, promotes exactly the first item for processing.
func (tq *TaskQueue) AddSequentialRuns(ctx context.Context, jobID string, items []models.TranscriptionQueueItem) ([]models.TranscriptionQueueItem, error) {
	return tq.AddSequentialRunsWithPreparation(ctx, jobID, items, nil)
}

// AddSequentialRunsWithPreparation executes preparation and queue admission
// under the same per-queue ownership lock. It is used to preserve the current
// transcript snapshot immediately before an idle promotion clears mutable job
// results, without racing job deletion or an immediate rerun.
func (tq *TaskQueue) AddSequentialRunsWithPreparation(ctx context.Context, jobID string, items []models.TranscriptionQueueItem, prepare func() error) ([]models.TranscriptionQueueItem, error) {
	if tq.runQueueRepo == nil {
		return nil, fmt.Errorf("sequential queue repository is not configured")
	}
	tq.jobsMutex.Lock()
	defer tq.jobsMutex.Unlock()
	if _, deleting := tq.deletingJobs[jobID]; deleting {
		return nil, ErrJobStateChanged
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			return nil, err
		}
	}
	if err := tq.runQueueRepo.Append(ctx, jobID, items); err != nil {
		return nil, err
	}
	if _, err := tq.promoteNext(ctx, jobID, true); err != nil {
		return nil, err
	}
	return tq.runQueueRepo.List(ctx, jobID, false)
}

// StartImmediateRun preserves the legacy one-off start behavior while making
// admission atomic with sequential queue additions and worker ownership.
func (tq *TaskQueue) StartImmediateRun(ctx context.Context, updatedJob *models.TranscriptionJob) error {
	if updatedJob == nil {
		return ErrJobStateChanged
	}
	tq.jobsMutex.Lock()
	defer tq.jobsMutex.Unlock()
	if _, deleting := tq.deletingJobs[updatedJob.ID]; deleting {
		return ErrJobStateChanged
	}
	if _, running := tq.runningJobs[updatedJob.ID]; running {
		return ErrJobStateChanged
	}

	current, err := tq.jobRepo.FindByID(ctx, updatedJob.ID)
	if err != nil {
		return err
	}
	if current.Status != models.StatusUploaded && current.Status != models.StatusCompleted && current.Status != models.StatusFailed {
		return ErrJobStateChanged
	}
	if tq.runQueueRepo != nil {
		items, listErr := tq.runQueueRepo.List(ctx, updatedJob.ID, false)
		if listErr != nil {
			return listErr
		}
		if len(items) > 0 {
			return ErrJobStateChanged
		}
	}

	updatedJob.Status = models.StatusPending
	if err := tq.jobRepo.Update(ctx, updatedJob); err != nil {
		return err
	}
	task := queuedTask{JobID: updatedJob.ID}
	if err := tq.enqueueTask(task, false); err != nil {
		if !errors.Is(err, errQueueFull) {
			return err
		}
		if tq.runQueueRepo == nil {
			return err
		}
		// Production queues have the durable reconciler enabled, which will retry
		// this persisted pending job when channel capacity becomes available.
		logger.Debug("Immediate run persisted for reconciled dispatch", "job_id", updatedJob.ID)
	}
	return nil
}

func (tq *TaskQueue) ListSequentialRuns(ctx context.Context, jobID string, includeTerminal bool) ([]models.TranscriptionQueueItem, error) {
	if tq.runQueueRepo == nil {
		return nil, fmt.Errorf("sequential queue repository is not configured")
	}
	return tq.runQueueRepo.List(ctx, jobID, includeTerminal)
}

func (tq *TaskQueue) HasSequentialRuns(ctx context.Context, jobID string) (bool, error) {
	if tq.runQueueRepo == nil {
		return false, nil
	}
	items, err := tq.runQueueRepo.List(ctx, jobID, false)
	return len(items) > 0, err
}

func (tq *TaskQueue) ReorderSequentialRuns(ctx context.Context, jobID string, orderedIDs []string) ([]models.TranscriptionQueueItem, error) {
	if tq.runQueueRepo == nil {
		return nil, fmt.Errorf("sequential queue repository is not configured")
	}
	if err := tq.runQueueRepo.ReorderQueued(ctx, jobID, orderedIDs); err != nil {
		return nil, err
	}
	return tq.runQueueRepo.List(ctx, jobID, false)
}

func (tq *TaskQueue) CancelSequentialRun(ctx context.Context, jobID, itemID string) ([]models.TranscriptionQueueItem, error) {
	if tq.runQueueRepo == nil {
		return nil, fmt.Errorf("sequential queue repository is not configured")
	}
	if err := tq.runQueueRepo.CancelQueued(ctx, jobID, itemID); err != nil {
		return nil, err
	}
	return tq.runQueueRepo.List(ctx, jobID, false)
}

func (tq *TaskQueue) ClearSequentialRuns(ctx context.Context, jobID string) ([]models.TranscriptionQueueItem, int64, error) {
	if tq.runQueueRepo == nil {
		return nil, 0, fmt.Errorf("sequential queue repository is not configured")
	}
	cleared, err := tq.runQueueRepo.ClearQueued(ctx, jobID)
	if err != nil {
		return nil, 0, err
	}
	items, err := tq.runQueueRepo.List(ctx, jobID, false)
	return items, cleared, err
}

func (tq *TaskQueue) DeleteSequentialRuns(ctx context.Context, jobID string) error {
	if tq.runQueueRepo == nil {
		return nil
	}
	return tq.runQueueRepo.DeleteByJobID(ctx, jobID)
}

// ReserveJobDeletion makes the read/check/delete admission decision atomic
// with worker claims, queue promotion, and new run submissions. The caller
// must invoke the returned release function after all file and database
// cleanup has finished.
func (tq *TaskQueue) ReserveJobDeletion(ctx context.Context, jobID string) (*models.TranscriptionJob, func(), error) {
	tq.jobsMutex.Lock()
	defer tq.jobsMutex.Unlock()

	if _, deleting := tq.deletingJobs[jobID]; deleting {
		return nil, nil, ErrJobStateChanged
	}
	if _, running := tq.runningJobs[jobID]; running {
		return nil, nil, ErrJobStateChanged
	}

	job, err := tq.jobRepo.FindByID(ctx, jobID)
	if err != nil {
		return nil, nil, err
	}
	if job.Status == models.StatusPending || job.Status == models.StatusProcessing {
		return nil, nil, ErrJobStateChanged
	}
	if tq.runQueueRepo != nil {
		items, listErr := tq.runQueueRepo.List(ctx, jobID, false)
		if listErr != nil {
			return nil, nil, listErr
		}
		if len(items) > 0 {
			return nil, nil, ErrJobStateChanged
		}
	}

	tq.deletingJobs[jobID] = struct{}{}
	var once sync.Once
	release := func() {
		once.Do(func() {
			tq.jobsMutex.Lock()
			delete(tq.deletingJobs, jobID)
			tq.jobsMutex.Unlock()
		})
	}
	return job, release, nil
}

func (tq *TaskQueue) promoteNext(ctx context.Context, jobID string, enqueue bool) (*models.TranscriptionQueueItem, error) {
	// Every runtime caller holds jobsMutex (startup recovery runs before workers
	// exist). Database status can become terminal slightly before a processor
	// returns, especially for multi-track work, so in-memory ownership is the
	// authoritative barrier for cleanup completion and successor promotion.
	if _, running := tq.runningJobs[jobID]; running {
		return nil, nil
	}
	if _, deleting := tq.deletingJobs[jobID]; deleting {
		return nil, ErrJobStateChanged
	}
	item, err := tq.runQueueRepo.PromoteNext(ctx, jobID)
	if err != nil || item == nil || !enqueue {
		return item, err
	}
	if err := tq.enqueueSequentialJob(jobID, item.ID); err != nil {
		return item, fmt.Errorf("enqueue promoted sequential run: %w", err)
	}
	return item, nil
}

// worker processes jobs from the channel
func (tq *TaskQueue) worker(id int) {
	defer tq.wg.Done()

	logger.Debug("Worker started", "worker_id", id)

	for {
		select {
		case task, ok := <-tq.jobChannel:
			if !ok {
				tq.releaseScheduledTask(task)
				logger.Debug("Worker stopped", "worker_id", id)
				return
			}
			if tq.ctx.Err() != nil {
				tq.releaseScheduledTask(task)
				logger.Debug("Worker stopped before claiming buffered work", "worker_id", id, "job_id", task.JobID)
				return
			}
			jobID := task.JobID

			logger.WorkerOperation(id, jobID, "start")

			job, err := tq.jobRepo.FindByID(context.Background(), jobID)
			if err != nil {
				tq.releaseScheduledTask(task)
				logger.Error("Failed to load queued job", "worker_id", id, "job_id", jobID, "error", err)
				continue
			}
			// Publish cancellation state atomically with the database claim. KillJob
			// takes the same lock, so it can never misclassify the narrow
			// claim-to-running-map window as a zombie and promote overlapping work.
			jobCtx, jobCancel := context.WithTimeout(tq.ctx, tq.jobTimeout)
			runningJob := &RunningJob{
				Cancel:      jobCancel,
				Process:     nil, // Will be set by registerProcess callback
				QueueItemID: task.QueueItemID,
			}
			tq.jobsMutex.Lock()
			if _, deleting := tq.deletingJobs[jobID]; deleting {
				tq.releaseScheduledTask(task)
				tq.jobsMutex.Unlock()
				jobCancel()
				logger.Info("Skipping queued task while job deletion is reserved", "worker_id", id, "job_id", jobID)
				continue
			}
			if _, alreadyRunning := tq.runningJobs[jobID]; alreadyRunning {
				tq.releaseScheduledTask(task)
				tq.jobsMutex.Unlock()
				jobCancel()
				logger.Info("Skipping queued task because the audio still has running ownership", "worker_id", id, "job_id", jobID, "queue_item_id", task.QueueItemID)
				continue
			}
			claimed, err := tq.claimTask(context.Background(), task, job)
			if claimed && err == nil {
				tq.runningJobs[jobID] = runningJob
			}
			// Keep the reservation until after the claim and running ownership are
			// published under jobsMutex. The reconciler uses the same lock, so it
			// cannot schedule another copy in the receive-to-claim window.
			tq.releaseScheduledTask(task)
			tq.jobsMutex.Unlock()
			if err != nil {
				jobCancel()
				logger.Error("Failed to claim queued job", "worker_id", id, "job_id", jobID, "queue_item_id", task.QueueItemID, "error", err)
				continue
			}
			if !claimed {
				jobCancel()
				logger.Info("Skipping stale or non-pending queued task", "worker_id", id, "job_id", jobID, "queue_item_id", task.QueueItemID, "status", job.Status)
				continue
			}

			// Register process callback
			registerProcess := func(cmd *exec.Cmd) {
				tq.jobsMutex.Lock()
				if job, exists := tq.runningJobs[jobID]; exists {
					job.Process = cmd
				}
				tq.jobsMutex.Unlock()
			}

			// Process the job with process registration
			err = tq.processor.ProcessJobWithProcess(jobCtx, jobID, registerProcess)
			tq.jobsMutex.Lock()
			if active := tq.runningJobs[jobID]; active == runningJob {
				active.Finishing = true
			}
			jobContextErr := jobCtx.Err()
			jobCancel()

			queueStatus := models.QueueStatusCompleted
			queueError := ""

			// Context cancellation takes precedence even when a processor returns nil.
			if jobContextErr == context.Canceled {
				queueError = "Job was cancelled by user"
				queueStatus = models.QueueStatusCancelled
				if tq.ctx.Err() != nil {
					queueError = "Job interrupted by server shutdown"
					queueStatus = models.QueueStatusFailed
				}
				logger.Info("Job cancelled", "worker_id", id, "job_id", jobID, "reason", queueError)
				if err := tq.updateJobStatus(jobID, models.StatusFailed); err != nil {
					logger.Error("Failed to update job status", "job_id", jobID, "error", err)
				}
				if err := tq.updateJobError(jobID, queueError); err != nil {
					logger.Error("Failed to update job error", "job_id", jobID, "error", err)
				}
			} else if jobContextErr == context.DeadlineExceeded {
				logger.Warn("Job timed out", "worker_id", id, "job_id", jobID, "timeout", tq.jobTimeout)
				queueStatus = models.QueueStatusFailed
				queueError = "Job exceeded the configured processing timeout"
				if err := tq.updateJobStatus(jobID, models.StatusFailed); err != nil {
					logger.Error("Failed to update job status", "job_id", jobID, "error", err)
				}
				if err := tq.updateJobError(jobID, "Job exceeded the configured processing timeout"); err != nil {
					logger.Error("Failed to update job error", "job_id", jobID, "error", err)
				}
			} else if err != nil {
				logger.Error("Job processing failed", "worker_id", id, "job_id", jobID, "error", err)
				queueStatus = models.QueueStatusFailed
				queueError = err.Error()
				if err := tq.updateJobStatus(jobID, models.StatusFailed); err != nil {
					logger.Error("Failed to update job status", "job_id", jobID, "error", err)
				}
				if err := tq.updateJobError(jobID, err.Error()); err != nil {
					logger.Error("Failed to update job error", "job_id", jobID, "error", err)
				}
			} else {
				logger.Debug("Job processed successfully", "worker_id", id, "job_id", jobID)
				if err := tq.updateJobStatus(jobID, models.StatusCompleted); err != nil {
					logger.Error("Failed to update job status", "job_id", jobID, "error", err)
				}
			}

			// The processor has fully returned (including its cleanup) before the
			// next per-audio run is promoted.
			tq.finalizeAndAdvance(jobID, task.QueueItemID, queueStatus, queueError, runningJob)
			tq.jobsMutex.Unlock()

		case <-tq.ctx.Done():
			logger.Debug("Worker stopped", "worker_id", id, "reason", "context_cancelled")
			return
		}
	}
}

type pendingJobClaimer interface {
	ClaimPending(ctx context.Context, jobID string) (bool, error)
}

type processingExecutionFinalizer interface {
	FailProcessingExecutions(ctx context.Context, jobID, reason string) error
}

func (tq *TaskQueue) claimTask(ctx context.Context, task queuedTask, job *models.TranscriptionJob) (bool, error) {
	if task.QueueItemID != "" {
		if tq.runQueueRepo == nil {
			return false, fmt.Errorf("sequential queue repository is not configured")
		}
		_, claimed, err := tq.runQueueRepo.ClaimPending(ctx, task.JobID, task.QueueItemID)
		return claimed, err
	}

	if job.Status != models.StatusPending {
		return false, nil
	}

	// A legacy channel entry can outlive cancellation. If an exact durable
	// queue item is now pending for the same audio, only its identity-bearing
	// channel entry may claim the job.
	if tq.runQueueRepo != nil {
		if _, err := tq.runQueueRepo.FindPending(ctx, task.JobID); err == nil {
			return false, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, err
		}
	}

	if claimer, ok := tq.jobRepo.(pendingJobClaimer); ok {
		return claimer.ClaimPending(ctx, task.JobID)
	}
	if err := tq.updateJobStatus(task.JobID, models.StatusProcessing); err != nil {
		return false, err
	}
	return true, nil
}

// finalizeAndAdvance runs while jobsMutex is held from the instant the
// processor returns. That makes terminalization, ownership removal, and
// successor promotion one observable transition to KillJob and other workers.
func (tq *TaskQueue) finalizeAndAdvance(jobID, queueItemID string, status models.TranscriptionQueueStatus, errorMessage string, owner *RunningJob) {
	if tq.runQueueRepo != nil {
		var executionID *string
		if queueItemID != "" {
			item, itemErr := tq.runQueueRepo.FindByID(context.Background(), jobID, queueItemID)
			if itemErr == nil {
				executionID = tq.matchingExecutionID(context.Background(), item)
			}
		}
		if err := tq.runQueueRepo.Finalize(context.Background(), jobID, queueItemID, status, errorMessage, executionID); err != nil {
			logger.Error("Failed to finalize sequential queue item", "job_id", jobID, "queue_item_id", queueItemID, "error", err)
		}
	}

	if tq.runningJobs[jobID] == owner {
		delete(tq.runningJobs, jobID)
	}
	if tq.runQueueRepo != nil {
		if _, err := tq.promoteNext(context.Background(), jobID, true); err != nil {
			logger.Error("Failed to promote next sequential run", "job_id", jobID, "error", err)
		}
	}
}

// KillJob aggressively terminates a running job or cancels a queued pending job
func (tq *TaskQueue) KillJob(jobID string) error {
	return tq.killJob(jobID, "", false)
}

// KillJobIfCurrent cancels only when the active run identity still matches
// what the caller reviewed. An empty expected ID explicitly means "the active
// run must be legacy/unqueued"; it is not the same as calling KillJob without
// a precondition. This prevents a delayed dialog for a legacy run from
// stopping a sequential successor after automatic promotion.
func (tq *TaskQueue) KillJobIfCurrent(jobID, expectedQueueItemID string) error {
	return tq.killJob(jobID, expectedQueueItemID, true)
}

func (tq *TaskQueue) killJob(jobID, expectedQueueItemID string, enforceTarget bool) error {
	tq.jobsMutex.Lock()
	runningJob, exists := tq.runningJobs[jobID]
	if !exists {
		defer tq.jobsMutex.Unlock()
		// If job is not in memory but exists in DB as processing, it's a zombie
		// We should still mark it as failed in DB
		logger.Warn("Job not found in running jobs map, checking DB status", "job_id", jobID)

		job, err := tq.jobRepo.FindByID(context.Background(), jobID)
		if err != nil {
			return fmt.Errorf("job %s not found: %v", jobID, err)
		}
		if enforceTarget {
			if tq.runQueueRepo == nil {
				if expectedQueueItemID != "" {
					return ErrQueueTargetChanged
				}
			} else {
				active, activeErr := tq.runQueueRepo.FindActive(context.Background(), jobID)
				if expectedQueueItemID == "" {
					if activeErr == nil || !errors.Is(activeErr, gorm.ErrRecordNotFound) {
						return ErrQueueTargetChanged
					}
				} else if activeErr != nil || active.ID != expectedQueueItemID {
					return ErrQueueTargetChanged
				}
			}
		}

		if job.Status == models.StatusProcessing {
			logger.Info("Found zombie job in DB, marking as failed", "job_id", jobID)
			if err := tq.updateJobStatus(jobID, models.StatusFailed); err != nil {
				logger.Error("Failed to update zombie job status", "job_id", jobID, "error", err)
			}
			if err := tq.updateJobError(jobID, "Job was forcefully terminated by user (zombie process)"); err != nil {
				logger.Error("Failed to update zombie job error", "job_id", jobID, "error", err)
			}
			tq.cancelPersistedActiveAndAdvance(jobID, "Job was forcefully terminated by user (zombie process)")
			return nil
		}

		if job.Status == models.StatusPending {
			logger.Info("Cancelling queued pending job", "job_id", jobID)
			if err := tq.updateJobStatus(jobID, models.StatusFailed); err != nil {
				logger.Error("Failed to update pending job status", "job_id", jobID, "error", err)
			}
			if err := tq.updateJobError(jobID, "Job was cancelled by user before processing"); err != nil {
				logger.Error("Failed to update pending job error", "job_id", jobID, "error", err)
			}
			tq.cancelPersistedActiveAndAdvance(jobID, "Job was cancelled by user before processing")
			return nil
		}

		return fmt.Errorf("job %s is not currently running", jobID)
	}
	if enforceTarget && runningJob.QueueItemID != expectedQueueItemID {
		tq.jobsMutex.Unlock()
		return ErrQueueTargetChanged
	}
	if runningJob.Finishing {
		tq.jobsMutex.Unlock()
		return fmt.Errorf("job %s has already finished processing and is finalizing", jobID)
	}
	runningProcess := runningJob.Process
	runningCancel := runningJob.Cancel
	// Cancellation is linearized while holding the same lock the worker needs
	// to declare itself finishing. Whichever side acquires the lock first owns
	// the outcome: a stop request or normal completion, never both.
	runningCancel()

	// Check if this is a multi-track job and handle accordingly
	if mtProcessor, ok := tq.processor.(MultiTrackJobProcessor); ok && mtProcessor.IsMultiTrackJob(jobID) {
		logger.Debug("Terminating multi-track job", "job_id", jobID)

		// Terminate all individual track jobs
		if err := mtProcessor.TerminateMultiTrackJob(jobID); err != nil {
			logger.Error("Failed to terminate multi-track job", "job_id", jobID, "error", err)
		}
	}

	// Finish every target-specific termination action before releasing
	// jobsMutex. The canceled worker needs this same lock to promote its
	// successor, so a stale stop can never resolve jobID against the successor's
	// newly registered multi-track transcriber or process.
	if runningProcess != nil && runningProcess.Process != nil {
		logger.Debug("Terminating process tree", "pid", runningProcess.Process.Pid, "job_id", jobID)
		if err := killProcessTree(runningProcess.Process); err != nil {
			log.Printf("Failed to terminate process tree for job %s: %v, trying direct kill()", jobID, err)
			_ = runningProcess.Process.Kill()
		}
	}
	tq.jobsMutex.Unlock()

	logger.Info("Killing job", "job_id", jobID)
	return nil
}

func (tq *TaskQueue) cancelPersistedActiveAndAdvance(jobID, reason string) {
	if tq.runQueueRepo == nil {
		return
	}
	active, err := tq.runQueueRepo.FindActive(context.Background(), jobID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Error("Failed to find active sequential run during cancellation", "job_id", jobID, "error", err)
			return
		}
	} else {
		if err := tq.runQueueRepo.MarkActiveCancelled(context.Background(), jobID, active.ID, reason); err != nil {
			logger.Error("Failed to cancel active sequential run", "job_id", jobID, "queue_item_id", active.ID, "error", err)
			return
		}
	}
	if _, err := tq.promoteNext(context.Background(), jobID, true); err != nil {
		logger.Error("Failed to promote sequential run after cancellation", "job_id", jobID, "error", err)
	}
}

// IsJobRunning checks if a job is currently being processed
func (tq *TaskQueue) IsJobRunning(jobID string) bool {
	tq.jobsMutex.RLock()
	defer tq.jobsMutex.RUnlock()

	_, exists := tq.runningJobs[jobID]
	return exists
}

// updateJobStatus updates the status of a job
func (tq *TaskQueue) updateJobStatus(jobID string, status models.JobStatus) error {
	return tq.jobRepo.UpdateStatus(context.Background(), jobID, status)
}

// updateJobError updates the error message of a job
func (tq *TaskQueue) updateJobError(jobID string, errorMsg string) error {
	return tq.jobRepo.UpdateError(context.Background(), jobID, errorMsg)
}

// GetJobStatus gets the status of a job
func (tq *TaskQueue) GetJobStatus(jobID string) (*models.TranscriptionJob, error) {
	return tq.jobRepo.FindByID(context.Background(), jobID)
}

// autoScaler monitors queue load and adjusts worker count
func (tq *TaskQueue) autoScaler() {
	defer tq.wg.Done()

	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()

	log.Println("Auto-scaler started")

	for {
		select {
		case <-ticker.C:
			tq.checkAndScale()
		case <-tq.ctx.Done():
			log.Println("Auto-scaler stopped")
			return
		}
	}
}

// checkAndScale evaluates current load and adjusts worker count
func (tq *TaskQueue) checkAndScale() {
	// Prevent too frequent scaling
	if time.Since(tq.lastScaleTime) < 1*time.Minute {
		return
	}

	queueSize := len(tq.jobChannel)
	currentWorkers := int(atomic.LoadInt64(&tq.currentWorkers))

	tq.jobsMutex.RLock()
	runningJobsCount := len(tq.runningJobs)
	tq.jobsMutex.RUnlock()

	// Scale up if queue is building up and we have capacity
	if queueSize > 10 && currentWorkers < tq.maxWorkers {
		newWorkerCount := currentWorkers + 1
		log.Printf("Scaling up workers: %d -> %d (queue size: %d)", currentWorkers, newWorkerCount, queueSize)

		atomic.StoreInt64(&tq.currentWorkers, int64(newWorkerCount))
		tq.wg.Add(1)
		go tq.worker(newWorkerCount - 1)
		tq.lastScaleTime = time.Now()

		// Scale down if queue is empty and minimal jobs running
	} else if queueSize == 0 && runningJobsCount <= 1 && currentWorkers > tq.minWorkers {
		newWorkerCount := currentWorkers - 1
		log.Printf("Scaling down workers: %d -> %d (queue size: %d, running: %d)",
			currentWorkers, newWorkerCount, queueSize, runningJobsCount)

		atomic.StoreInt64(&tq.currentWorkers, int64(newWorkerCount))
		tq.lastScaleTime = time.Now()

		// Note: We don't actively stop workers here. They will naturally exit
		// when no more jobs are available and the queue empties.
	}
}

// GetQueueStats returns queue statistics
func (tq *TaskQueue) GetQueueStats() map[string]interface{} {
	ctx := context.Background()
	pendingCount, _ := tq.jobRepo.CountByStatus(ctx, models.StatusPending)
	processingCount, _ := tq.jobRepo.CountByStatus(ctx, models.StatusProcessing)
	completedCount, _ := tq.jobRepo.CountByStatus(ctx, models.StatusCompleted)
	failedCount, _ := tq.jobRepo.CountByStatus(ctx, models.StatusFailed)

	tq.jobsMutex.RLock()
	runningJobsCount := len(tq.runningJobs)
	tq.jobsMutex.RUnlock()

	return map[string]interface{}{
		"queue_size":      len(tq.jobChannel),
		"queue_capacity":  cap(tq.jobChannel),
		"current_workers": int(atomic.LoadInt64(&tq.currentWorkers)),
		"min_workers":     tq.minWorkers,
		"max_workers":     tq.maxWorkers,
		"auto_scale":      tq.autoScale,
		"running_jobs":    runningJobsCount,
		"pending_jobs":    pendingCount,
		"processing_jobs": processingCount,
		"completed_jobs":  completedCount,
		"failed_jobs":     failedCount,
	}
}

// ResetZombieJobs finds jobs stuck in processing state from previous runs and marks them as failed
func (tq *TaskQueue) ResetZombieJobs() {
	// Find all jobs with status "processing"
	zombieJobs, err := tq.jobRepo.FindByStatus(context.Background(), models.StatusProcessing)
	if err != nil {
		logger.Error("Failed to scan for zombie jobs", "error", err)
		return
	}

	if len(zombieJobs) == 0 {
		return
	}

	logger.Info("Found zombie jobs from previous run", "count", len(zombieJobs))

	for _, job := range zombieJobs {
		logger.Info("Resetting zombie job", "job_id", job.ID)

		// Mark as failed
		if err := tq.updateJobStatus(job.ID, models.StatusFailed); err != nil {
			logger.Error("Failed to update zombie job status", "job_id", job.ID, "error", err)
			continue
		}

		// Update error message
		if err := tq.updateJobError(job.ID, "Job interrupted by server restart"); err != nil {
			logger.Error("Failed to update zombie job error message", "job_id", job.ID, "error", err)
		}
		if finalizer, ok := tq.jobRepo.(processingExecutionFinalizer); ok {
			if err := finalizer.FailProcessingExecutions(context.Background(), job.ID, "Job interrupted by server restart"); err != nil {
				logger.Error("Failed to reconcile interrupted execution history", "job_id", job.ID, "error", err)
			}
		}
	}
}

func (tq *TaskQueue) recoverSequentialRuns() {
	if tq.runQueueRepo == nil {
		return
	}
	tq.jobsMutex.Lock()
	defer tq.jobsMutex.Unlock()

	if _, err := tq.runQueueRepo.FailInterrupted(context.Background(), "Run interrupted by server restart"); err != nil {
		logger.Error("Failed to repair interrupted sequential runs", "error", err)
		return
	}

	jobIDs, err := tq.runQueueRepo.ListJobIDsWithQueued(context.Background())
	if err != nil {
		logger.Error("Failed to scan waiting sequential runs during startup", "error", err)
		return
	}
	for _, jobID := range jobIDs {
		if _, err := tq.promoteNext(context.Background(), jobID, false); err != nil {
			logger.Error("Failed to promote sequential run during startup", "job_id", jobID, "error", err)
		}
	}
}

// recoverPendingJobs enqueues pending jobs from previous server runs
// This runs ONCE at startup, not repeatedly like the old scanner
func (tq *TaskQueue) recoverPendingJobs() {
	pendingJobs, err := tq.jobRepo.FindByStatus(context.Background(), models.StatusPending)
	if err != nil {
		logger.Error("Failed to scan for pending jobs during startup recovery", "error", err)
		return
	}

	if len(pendingJobs) == 0 {
		return
	}

	logger.Info("Recovering pending jobs from previous server run", "count", len(pendingJobs))

	for _, job := range pendingJobs {
		task := queuedTask{JobID: job.ID}
		if tq.runQueueRepo != nil {
			pending, findErr := tq.runQueueRepo.FindPending(context.Background(), job.ID)
			if findErr == nil {
				task.QueueItemID = pending.ID
			} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
				logger.Error("Failed to resolve pending sequential run during startup", "job_id", job.ID, "error", findErr)
				continue
			}
		}
		if err := tq.enqueueTask(task, false); err != nil {
			// Startup must not wait behind long-running recovered work. Pending
			// state is durable, and the reconciler will retry every undispatched
			// identity after the server is available.
			logger.Warn("Pending job recovery deferred", "job_id", job.ID, "queue_item_id", task.QueueItemID, "error", err)
			continue
		}
		logger.Debug("Recovered pending job", "job_id", job.ID, "queue_item_id", task.QueueItemID)
	}
}

// sequentialReconciler is a low-frequency durability safety net. Normal
// transitions are event driven; this loop repairs the narrow case where a
// transient database/channel failure happened after a processor returned.
func (tq *TaskQueue) sequentialReconciler() {
	defer tq.wg.Done()
	ticker := time.NewTicker(tq.reconcileEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			tq.reconcileSequentialRuns()
		case <-tq.ctx.Done():
			return
		}
	}
}

func (tq *TaskQueue) reconcileSequentialRuns() {
	if tq.runQueueRepo == nil || tq.ctx.Err() != nil {
		return
	}

	tq.jobsMutex.Lock()
	defer tq.jobsMutex.Unlock()
	ctx := context.Background()

	processing, err := tq.runQueueRepo.ListProcessing(ctx)
	if err != nil {
		logger.Error("Failed to inspect processing sequential runs", "error", err)
		return
	}
	for _, item := range processing {
		if _, running := tq.runningJobs[item.TranscriptionJobID]; running {
			continue
		}
		job, findErr := tq.jobRepo.FindByID(ctx, item.TranscriptionJobID)
		if findErr != nil {
			logger.Error("Failed to load orphaned sequential run parent", "job_id", item.TranscriptionJobID, "error", findErr)
			continue
		}

		reason := "Sequential run lost worker ownership"
		queueStatus := models.QueueStatusFailed
		if job.Status == models.StatusCompleted {
			queueStatus = models.QueueStatusCompleted
			reason = ""
		} else if job.ErrorMessage != nil && *job.ErrorMessage != "" {
			reason = *job.ErrorMessage
			if strings.Contains(strings.ToLower(reason), "cancel") {
				queueStatus = models.QueueStatusCancelled
			}
		}
		if job.Status == models.StatusProcessing {
			_ = tq.updateJobStatus(job.ID, models.StatusFailed)
			_ = tq.updateJobError(job.ID, reason)
			if finalizer, ok := tq.jobRepo.(processingExecutionFinalizer); ok {
				_ = finalizer.FailProcessingExecutions(ctx, job.ID, reason)
			}
		}

		executionID := tq.matchingExecutionID(ctx, &item)
		if finalizeErr := tq.runQueueRepo.Finalize(ctx, item.TranscriptionJobID, item.ID, queueStatus, reason, executionID); finalizeErr != nil {
			logger.Error("Failed to reconcile sequential run", "job_id", item.TranscriptionJobID, "queue_item_id", item.ID, "error", finalizeErr)
		}
	}

	// Normal sequential terminalization updates parent and item transactionally.
	// This repair remains for interrupted upgrades and independently injected
	// legacy repositories that may have persisted only one side. Claim and
	// running-map publication use jobsMutex too, making a processing parent with
	// no in-memory owner unambiguously orphaned while this lock is held.
	queueJobIDs, err := tq.runQueueRepo.ListJobIDsWithItems(ctx)
	if err != nil {
		logger.Error("Failed to inspect sequential queue parents", "error", err)
		return
	}
	for _, jobID := range queueJobIDs {
		if _, running := tq.runningJobs[jobID]; running {
			continue
		}
		job, findErr := tq.jobRepo.FindByID(ctx, jobID)
		if findErr != nil {
			if !errors.Is(findErr, gorm.ErrRecordNotFound) {
				logger.Error("Failed to inspect sequential queue parent", "job_id", jobID, "error", findErr)
			}
			continue
		}
		if job.Status != models.StatusProcessing {
			continue
		}
		reason := "Sequential run lost worker ownership"
		if err := tq.updateJobStatus(job.ID, models.StatusFailed); err != nil {
			logger.Error("Failed to repair orphaned parent status", "job_id", job.ID, "error", err)
			continue
		}
		if err := tq.updateJobError(job.ID, reason); err != nil {
			logger.Error("Failed to repair orphaned parent error", "job_id", job.ID, "error", err)
		}
		if finalizer, ok := tq.jobRepo.(processingExecutionFinalizer); ok {
			if err := finalizer.FailProcessingExecutions(ctx, job.ID, reason); err != nil {
				logger.Error("Failed to repair orphaned execution history", "job_id", job.ID, "error", err)
			}
		}
	}

	queuedJobIDs, err := tq.runQueueRepo.ListJobIDsWithQueued(ctx)
	if err != nil {
		logger.Error("Failed to inspect waiting sequential runs", "error", err)
		return
	}
	for _, jobID := range queuedJobIDs {
		_, promoteErr := tq.promoteNext(ctx, jobID, true)
		if promoteErr != nil {
			logger.Error("Failed to reconcile waiting sequential run", "job_id", jobID, "error", promoteErr)
		}
	}

	pendingJobs, err := tq.jobRepo.FindByStatus(ctx, models.StatusPending)
	if err != nil {
		logger.Error("Failed to inspect pending jobs during reconciliation", "error", err)
		return
	}
	for _, job := range pendingJobs {
		if _, running := tq.runningJobs[job.ID]; running {
			continue
		}
		task := queuedTask{JobID: job.ID}
		if pending, findErr := tq.runQueueRepo.FindPending(ctx, job.ID); findErr == nil {
			task.QueueItemID = pending.ID
		} else if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			continue
		}
		_ = tq.enqueueTask(task, false)
	}
}

func (tq *TaskQueue) matchingExecutionID(ctx context.Context, item *models.TranscriptionQueueItem) *string {
	if item == nil || item.StartedAt == nil {
		return nil
	}
	execution, err := tq.jobRepo.FindLatestExecution(ctx, item.TranscriptionJobID)
	if err != nil || execution.StartedAt.Before(*item.StartedAt) {
		return nil
	}
	return &execution.ID
}
