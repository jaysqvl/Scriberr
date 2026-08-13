package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"scriberr/internal/config"
	"scriberr/internal/database"
	"scriberr/internal/models"
	"scriberr/internal/processutil"
	"scriberr/internal/resource"

	"github.com/gin-gonic/gin"
)

var ErrMediaCapacity = errors.New("media processing capacity is full")

const defaultMaxUploadBytes int64 = 20 * 1024 * 1024 * 1024

type resourceAdmission struct {
	mediaSlots   chan struct{}
	uploadSlots  chan struct{}
	timeout      time.Duration
	diskMu       sync.Mutex
	reservedDisk int64
	sessionMu    sync.Mutex
}

func newResourceAdmission(cfg *config.Config) *resourceAdmission {
	concurrency := 2
	timeout := 2 * time.Hour
	if cfg != nil {
		if cfg.MaxConcurrentMedia > 0 {
			concurrency = cfg.MaxConcurrentMedia
		}
		if cfg.MediaTimeoutMinutes > 0 {
			timeout = time.Duration(cfg.MediaTimeoutMinutes) * time.Minute
		}
	}
	uploadConcurrency := 8
	if cfg != nil && cfg.MaxActiveUploads > 0 {
		uploadConcurrency = cfg.MaxActiveUploads
	}
	return &resourceAdmission{
		mediaSlots:  make(chan struct{}, concurrency),
		uploadSlots: make(chan struct{}, uploadConcurrency),
		timeout:     timeout,
	}
}

func (a *resourceAdmission) tryAcquire() (func(), error) {
	select {
	case a.mediaSlots <- struct{}{}:
		return func() { <-a.mediaSlots }, nil
	default:
		return nil, ErrMediaCapacity
	}
}

func (a *resourceAdmission) tryAcquireUpload() (func(), error) {
	select {
	case a.uploadSlots <- struct{}{}:
		return func() { <-a.uploadSlots }, nil
	default:
		return nil, ErrMediaCapacity
	}
}

func (h *Handler) runMediaCommand(ctx context.Context, name string, args ...string) error {
	release, err := h.resourceAdmission.tryAcquire()
	if err != nil {
		return err
	}
	defer release()

	processCtx, cancel := context.WithTimeout(ctx, h.resourceAdmission.timeout)
	defer cancel()
	command := processutil.CommandContext(processCtx, name, args...)
	return command.Run()
}

func (h *Handler) maxUploadBytes() int64 {
	if h.config != nil && h.config.MaxUploadBytes > 0 {
		return h.config.MaxUploadBytes
	}
	return defaultMaxUploadBytes
}

func (h *Handler) limitUploadBody(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.maxUploadBytes()+1024*1024)
}

func (h *Handler) beginMultipartUpload(c *gin.Context, path string) (func(), bool) {
	h.limitUploadBody(c)
	if c.Request.ContentLength > h.maxUploadBytes()+1024*1024 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Upload exceeds the configured size limit"})
		return nil, false
	}

	releaseSlot, err := h.resourceAdmission.tryAcquireUpload()
	if err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Upload capacity is full"})
		return nil, false
	}

	requestedBytes := c.Request.ContentLength
	if requestedBytes <= 0 || requestedBytes > h.maxUploadBytes() {
		requestedBytes = h.maxUploadBytes()
	}
	releaseDisk, err := h.reserveDiskCapacity(path, requestedBytes*2)
	if err != nil {
		releaseSlot()
		c.JSON(http.StatusInsufficientStorage, gin.H{"error": err.Error()})
		return nil, false
	}

	return func() {
		releaseDisk()
		releaseSlot()
	}, true
}

func (h *Handler) acceptUploadSize(c *gin.Context, path string, size int64) bool {
	if size <= 0 || size > h.maxUploadBytes() {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Upload exceeds the configured size limit"})
		return false
	}
	return true
}

func (h *Handler) reserveDiskCapacity(path string, requestedBytes int64) (func(), error) {
	a := h.resourceAdmission
	a.diskMu.Lock()
	defer a.diskMu.Unlock()

	resumableBytes, err := h.activeResumableReservationBytes()
	if err != nil {
		return nil, fmt.Errorf("check active upload reservations: %w", err)
	}
	if err := h.ensureUploadCapacity(path, requestedBytes, a.reservedDisk+resumableBytes*2); err != nil {
		return nil, err
	}
	a.reservedDisk += requestedBytes
	return func() {
		a.diskMu.Lock()
		a.reservedDisk -= requestedBytes
		if a.reservedDisk < 0 {
			a.reservedDisk = 0
		}
		a.diskMu.Unlock()
	}, nil
}

func (h *Handler) reserveYouTubeCapacity(path string) (int64, func(), error) {
	a := h.resourceAdmission
	a.diskMu.Lock()
	defer a.diskMu.Unlock()

	if err := os.MkdirAll(path, 0755); err != nil {
		return 0, nil, err
	}
	freeBytes, err := resource.FreeBytes(path)
	if err != nil {
		return 0, nil, fmt.Errorf("check free disk space: %w", err)
	}
	minimumFree := int64(0)
	if h.config != nil {
		minimumFree = h.config.MinFreeDiskBytes
	}
	resumableBytes, err := h.activeResumableReservationBytes()
	if err != nil {
		return 0, nil, fmt.Errorf("check active upload reservations: %w", err)
	}
	available := int64(freeBytes) - minimumFree - a.reservedDisk - resumableBytes*2
	maxDownloadBytes := available / 2
	if maxDownloadBytes > h.maxUploadBytes() {
		maxDownloadBytes = h.maxUploadBytes()
	}
	if maxDownloadBytes < 1024*1024 {
		return 0, nil, fmt.Errorf("insufficient free disk space for download")
	}

	reservedBytes := maxDownloadBytes * 2
	a.reservedDisk += reservedBytes
	return maxDownloadBytes, func() {
		a.diskMu.Lock()
		a.reservedDisk -= reservedBytes
		if a.reservedDisk < 0 {
			a.reservedDisk = 0
		}
		a.diskMu.Unlock()
	}, nil
}

func (h *Handler) activeResumableReservationBytes() (int64, error) {
	if database.DB == nil {
		return 0, fmt.Errorf("database is unavailable")
	}
	var reservedBytes int64
	err := database.DB.Table("upload_session_files AS files").
		Select("COALESCE(SUM(files.size), 0)").
		Joins("JOIN upload_sessions AS sessions ON sessions.id = files.upload_session_id").
		Where("sessions.status = ? AND sessions.expires_at > ?", models.UploadSessionActive, time.Now()).
		Scan(&reservedBytes).Error
	return reservedBytes, err
}

func (h *Handler) ensureUploadCapacity(path string, requestedBytes, reservedBytes int64) error {
	if requestedBytes <= 0 {
		return fmt.Errorf("upload reservation must be positive")
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return err
	}
	freeBytes, err := resource.FreeBytes(path)
	if err != nil {
		return fmt.Errorf("check free disk space: %w", err)
	}
	reserve := int64(0)
	if h.config != nil {
		reserve = h.config.MinFreeDiskBytes
	}
	required := requestedBytes + reservedBytes + reserve
	if required < 0 || uint64(required) > freeBytes {
		return fmt.Errorf("insufficient free disk space for upload")
	}
	return nil
}

type boundedBuffer struct {
	data      []byte
	remaining int
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{data: make([]byte, 0, limit), remaining: limit}
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	if b.remaining > 0 {
		if len(value) > b.remaining {
			value = value[:b.remaining]
		}
		b.data = append(b.data, value...)
		b.remaining -= len(value)
	}
	return originalLength, nil
}

func (b *boundedBuffer) String() string {
	return string(b.data)
}
