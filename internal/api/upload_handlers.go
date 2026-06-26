package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"scriberr/internal/database"
	"scriberr/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	uploadTokenBytes          = 32
	maxUploadSessionFiles     = 256
	maxUploadChunksPerFile    = 10000
	maxUploadTitleLength      = 500
	maxUploadProfileLength    = 200
	maxUploadParametersBytes  = 64 * 1024
	maxUploadFilenameLength   = 512
	maxUploadContentTypeBytes = 255
)

type createUploadSessionRequest struct {
	Kind           models.UploadSessionKind `json:"kind"`
	Title          string                   `json:"title"`
	ProfileName    string                   `json:"profile_name"`
	ParametersJSON json.RawMessage          `json:"parameters,omitempty"`
	Files          []uploadSessionFileInput `json:"files"`
}

type uploadSessionFileInput struct {
	ID           string                `json:"id"`
	Role         models.UploadFileRole `json:"role"`
	Name         string                `json:"name"`
	ContentType  string                `json:"content_type"`
	Size         int64                 `json:"size"`
	LastModified int64                 `json:"last_modified"`
}

type uploadSessionResponse struct {
	ID        string                     `json:"id"`
	Kind      models.UploadSessionKind   `json:"kind"`
	Status    models.UploadSessionStatus `json:"status"`
	Token     string                     `json:"token,omitempty"`
	ChunkSize int64                      `json:"chunk_size"`
	ExpiresAt time.Time                  `json:"expires_at"`
	ResultID  *string                    `json:"result_id,omitempty"`
	Files     []uploadSessionFileStatus  `json:"files"`
}

type uploadSessionFileStatus struct {
	ID             string                `json:"id"`
	Role           models.UploadFileRole `json:"role"`
	Name           string                `json:"name"`
	ContentType    string                `json:"content_type"`
	Size           int64                 `json:"size"`
	ChunkCount     int                   `json:"chunk_count"`
	ReceivedBytes  int64                 `json:"received_bytes"`
	AcceptedChunks []int                 `json:"accepted_chunks"`
	MissingChunks  []int                 `json:"missing_chunks"`
}

// CreateUploadSession starts a resumable upload session.
func (h *Handler) CreateUploadSession(c *gin.Context) {
	h.cleanupExpiredUploadSessions()

	var req createUploadSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid upload session request"})
		return
	}

	if err := validateUploadSessionRequest(req, h.config.UploadChunkSizeBytes); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	token, tokenHash, err := newUploadToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create upload token"})
		return
	}

	var title *string
	if strings.TrimSpace(req.Title) != "" {
		v := strings.TrimSpace(req.Title)
		title = &v
	}

	var profileName *string
	if strings.TrimSpace(req.ProfileName) != "" {
		v := strings.TrimSpace(req.ProfileName)
		profileName = &v
	}

	var paramsJSON *string
	if len(req.ParametersJSON) > 0 && string(req.ParametersJSON) != "null" {
		v := string(req.ParametersJSON)
		paramsJSON = &v
	}

	session := models.UploadSession{
		ID:             uuid.New().String(),
		Kind:           req.Kind,
		Status:         models.UploadSessionActive,
		TokenHash:      tokenHash,
		Title:          title,
		ProfileName:    profileName,
		ParametersJSON: paramsJSON,
		ChunkSize:      h.config.UploadChunkSizeBytes,
		ExpiresAt:      time.Now().Add(time.Duration(h.config.UploadSessionTTLHours) * time.Hour),
	}

	files := make([]models.UploadSessionFile, 0, len(req.Files))
	for i, file := range req.Files {
		fileID := strings.TrimSpace(file.ID)
		if fileID == "" {
			fileID = fmt.Sprintf("%s-%d", file.Role, i)
		}
		if !validUploadFileID(fileID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid file id %q", fileID)})
			return
		}

		files = append(files, models.UploadSessionFile{
			ID:              fileID,
			UploadSessionID: session.ID,
			Role:            file.Role,
			OriginalName:    filepath.Base(file.Name),
			ContentType:     file.ContentType,
			Size:            file.Size,
			LastModified:    file.LastModified,
			ChunkCount:      chunkCount(file.Size, session.ChunkSize),
			ReceivedChunks:  "[]",
		})
	}

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		return tx.Create(&files).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create upload session"})
		return
	}

	session.Files = files
	c.JSON(http.StatusOK, buildUploadSessionResponse(session, token))
}

// GetUploadSession returns resumable upload progress for a session.
func (h *Handler) GetUploadSession(c *gin.Context) {
	session, err := h.loadUploadSession(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Upload session not found"})
		return
	}

	c.JSON(http.StatusOK, buildUploadSessionResponse(*session, ""))
}

// UploadChunk accepts one raw file chunk.
func (h *Handler) UploadChunk(c *gin.Context) {
	session, err := h.loadUploadSession(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Upload session not found"})
		return
	}
	if !h.validateUploadToken(c, session) {
		return
	}
	if err := ensureSessionWritable(*session); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	fileID := c.Param("file_id")
	file, ok := findUploadSessionFile(session.Files, fileID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Upload file not found"})
		return
	}

	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 0 || index >= file.ChunkCount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid chunk index"})
		return
	}

	start, end, total, err := parseContentRange(c.GetHeader("Content-Range"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if total != file.Size {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Content-Range total does not match file size"})
		return
	}

	expectedStart := int64(index) * session.ChunkSize
	expectedEnd := minInt64(expectedStart+session.ChunkSize, file.Size) - 1
	if start != expectedStart || end != expectedEnd {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Content-Range does not match chunk index"})
		return
	}
	expectedSize := end - start + 1
	if c.Request.ContentLength > expectedSize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Chunk body is larger than expected"})
		return
	}

	chunkHash := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Chunk-SHA256")))
	if len(chunkHash) != sha256.Size*2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "X-Chunk-SHA256 header is required"})
		return
	}

	chunkPath := h.uploadChunkPath(session.ID, file.ID, index)
	if existingOK, existingErr := existingChunkMatches(chunkPath, chunkHash, expectedSize); existingErr != nil {
		c.JSON(http.StatusConflict, gin.H{"error": existingErr.Error()})
		return
	} else if existingOK {
		_ = h.markChunkReceived(file, index, expectedSize)
		c.JSON(http.StatusOK, gin.H{"accepted": true, "duplicate": true})
		return
	}

	if err := os.MkdirAll(filepath.Dir(chunkPath), 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create chunk directory"})
		return
	}

	tmpPath := fmt.Sprintf("%s.%s.tmp", chunkPath, uuid.New().String())
	if err := writeChunk(tmpPath, c.Request.Body, expectedSize, chunkHash); err != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := os.Rename(tmpPath, chunkPath); err != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store chunk"})
		return
	}

	if err := h.markChunkReceived(file, index, expectedSize); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update chunk status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"accepted": true})
}

// CompleteUploadSession assembles all files and creates the final Scriberr job.
func (h *Handler) CompleteUploadSession(c *gin.Context) {
	session, err := h.loadUploadSession(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Upload session not found"})
		return
	}
	if !h.validateUploadToken(c, session) {
		return
	}

	if session.Status == models.UploadSessionCompleted && session.ResultID != nil {
		h.respondWithCompletedUpload(c, session)
		return
	}
	if err := ensureSessionWritable(*session); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	for _, file := range session.Files {
		accepted, _ := parseChunkList(file.ReceivedChunks)
		if len(accepted) != file.ChunkCount {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("File %s is missing chunks", file.ID)})
			return
		}
	}

	assembledFiles := make([]assembledUploadFile, 0, len(session.Files))
	for _, file := range session.Files {
		path, err := h.assembleUploadFile(*session, file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		assembledFiles = append(assembledFiles, assembledUploadFile{
			ID:           file.ID,
			Role:         file.Role,
			OriginalName: file.OriginalName,
			ContentType:  file.ContentType,
			Path:         path,
			Size:         file.Size,
		})
	}

	resultID, resultType, result, err := h.finalizeAssembledUpload(c, session, assembledFiles)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := database.DB.Model(&models.UploadSession{}).Where("id = ?", session.ID).Updates(map[string]interface{}{
		"status":      models.UploadSessionCompleted,
		"result_id":   resultID,
		"result_type": resultType,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to complete upload session"})
		return
	}

	_ = os.RemoveAll(h.uploadSessionRoot(session.ID))
	c.JSON(http.StatusOK, result)
}

// CancelUploadSession cancels an active upload and removes staged chunks.
func (h *Handler) CancelUploadSession(c *gin.Context) {
	session, err := h.loadUploadSession(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Upload session not found"})
		return
	}
	if !h.validateUploadToken(c, session) {
		return
	}

	if err := database.DB.Model(&models.UploadSession{}).Where("id = ?", session.ID).Update("status", models.UploadSessionCancelled).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel upload session"})
		return
	}
	_ = os.RemoveAll(h.uploadSessionRoot(session.ID))
	c.JSON(http.StatusOK, gin.H{"cancelled": true})
}

func validateUploadSessionRequest(req createUploadSessionRequest, chunkSize int64) error {
	switch req.Kind {
	case models.UploadKindAudio, models.UploadKindVideo, models.UploadKindQuick, models.UploadKindMultiTrack, models.UploadKindSubmit:
	default:
		return fmt.Errorf("Unsupported upload kind")
	}
	if len(req.Files) == 0 {
		return fmt.Errorf("At least one file is required")
	}
	if len(req.Files) > maxUploadSessionFiles {
		return fmt.Errorf("Upload session supports at most %d files", maxUploadSessionFiles)
	}
	if strings.TrimSpace(req.Title) != "" && len(req.Title) > maxUploadTitleLength {
		return fmt.Errorf("Upload title is too long")
	}
	if strings.TrimSpace(req.ProfileName) != "" && len(req.ProfileName) > maxUploadProfileLength {
		return fmt.Errorf("Profile name is too long")
	}
	if len(req.ParametersJSON) > maxUploadParametersBytes {
		return fmt.Errorf("Upload parameters are too large")
	}
	if chunkSize <= 0 {
		return fmt.Errorf("Invalid upload chunk size")
	}

	roleCounts := map[models.UploadFileRole]int{}
	seenIDs := map[string]bool{}
	for i, file := range req.Files {
		switch file.Role {
		case models.UploadFileRoleAudio, models.UploadFileRoleVideo, models.UploadFileRoleAup, models.UploadFileRoleTrack:
		default:
			return fmt.Errorf("File %d has invalid role", i)
		}
		if file.Size <= 0 {
			return fmt.Errorf("File %d has invalid size", i)
		}
		if strings.TrimSpace(file.Name) == "" {
			return fmt.Errorf("File %d is missing a name", i)
		}
		if len(file.Name) > maxUploadFilenameLength {
			return fmt.Errorf("File %d name is too long", i)
		}
		if len(file.ContentType) > maxUploadContentTypeBytes {
			return fmt.Errorf("File %d content type is too long", i)
		}
		if chunks := chunkCount(file.Size, chunkSize); chunks <= 0 || chunks > maxUploadChunksPerFile {
			return fmt.Errorf("File %d has too many chunks", i)
		}
		if file.ID != "" {
			if !validUploadFileID(file.ID) {
				return fmt.Errorf("File %d has invalid id", i)
			}
			if seenIDs[file.ID] {
				return fmt.Errorf("Duplicate file id %q", file.ID)
			}
			seenIDs[file.ID] = true
		}
		roleCounts[file.Role]++
	}

	switch req.Kind {
	case models.UploadKindAudio, models.UploadKindQuick, models.UploadKindSubmit:
		if len(req.Files) != 1 || roleCounts[models.UploadFileRoleAudio] != 1 {
			return fmt.Errorf("%s uploads require exactly one audio file", req.Kind)
		}
	case models.UploadKindVideo:
		if len(req.Files) != 1 || roleCounts[models.UploadFileRoleVideo] != 1 {
			return fmt.Errorf("Video uploads require exactly one video file")
		}
	case models.UploadKindMultiTrack:
		if roleCounts[models.UploadFileRoleAup] != 1 || roleCounts[models.UploadFileRoleTrack] == 0 {
			return fmt.Errorf("Multi-track uploads require one .aup file and at least one track")
		}
	}
	return nil
}

func (h *Handler) loadUploadSession(id string) (*models.UploadSession, error) {
	var session models.UploadSession
	if err := database.DB.Preload("Files").Where("id = ?", id).First(&session).Error; err != nil {
		return nil, err
	}
	sort.Slice(session.Files, func(i, j int) bool {
		return session.Files[i].ID < session.Files[j].ID
	})
	return &session, nil
}

func (h *Handler) validateUploadToken(c *gin.Context, session *models.UploadSession) bool {
	token := c.GetHeader("X-Upload-Token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Missing upload token"})
		return false
	}
	if !uploadTokenMatches(token, session.TokenHash) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid upload token"})
		return false
	}
	return true
}

func ensureSessionWritable(session models.UploadSession) error {
	if session.Status != models.UploadSessionActive {
		return fmt.Errorf("Upload session is not active")
	}
	if time.Now().After(session.ExpiresAt) {
		return fmt.Errorf("Upload session has expired")
	}
	return nil
}

func buildUploadSessionResponse(session models.UploadSession, token string) uploadSessionResponse {
	files := make([]uploadSessionFileStatus, 0, len(session.Files))
	for _, file := range session.Files {
		accepted, _ := parseChunkList(file.ReceivedChunks)
		files = append(files, uploadSessionFileStatus{
			ID:             file.ID,
			Role:           file.Role,
			Name:           file.OriginalName,
			ContentType:    file.ContentType,
			Size:           file.Size,
			ChunkCount:     file.ChunkCount,
			ReceivedBytes:  file.ReceivedBytes,
			AcceptedChunks: accepted,
			MissingChunks:  missingChunks(file.ChunkCount, accepted),
		})
	}

	return uploadSessionResponse{
		ID:        session.ID,
		Kind:      session.Kind,
		Status:    session.Status,
		Token:     token,
		ChunkSize: session.ChunkSize,
		ExpiresAt: session.ExpiresAt,
		ResultID:  session.ResultID,
		Files:     files,
	}
}

func findUploadSessionFile(files []models.UploadSessionFile, id string) (models.UploadSessionFile, bool) {
	for _, file := range files {
		if file.ID == id {
			return file, true
		}
	}
	return models.UploadSessionFile{}, false
}

func (h *Handler) markChunkReceived(file models.UploadSessionFile, index int, size int64) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		var current models.UploadSessionFile
		if err := tx.Where("id = ? AND upload_session_id = ?", file.ID, file.UploadSessionID).First(&current).Error; err != nil {
			return err
		}

		accepted, _ := parseChunkList(current.ReceivedChunks)
		for _, existing := range accepted {
			if existing == index {
				return nil
			}
		}

		accepted = append(accepted, index)
		sort.Ints(accepted)
		data, err := json.Marshal(accepted)
		if err != nil {
			return err
		}

		return tx.Model(&models.UploadSessionFile{}).Where("id = ? AND upload_session_id = ?", file.ID, file.UploadSessionID).Updates(map[string]interface{}{
			"received_chunks": string(data),
			"received_bytes":  gorm.Expr("received_bytes + ?", size),
		}).Error
	})
}

func (h *Handler) assembleUploadFile(session models.UploadSession, file models.UploadSessionFile) (string, error) {
	assembledDir := filepath.Join(h.uploadSessionRoot(session.ID), "assembled")
	if err := os.MkdirAll(assembledDir, 0755); err != nil {
		return "", fmt.Errorf("Failed to create assembly directory")
	}

	finalPath := filepath.Join(assembledDir, file.ID+"-"+safeFilename(file.OriginalName))
	tmpPath := finalPath + ".tmp"

	out, err := os.Create(tmpPath)
	if err != nil {
		return "", fmt.Errorf("Failed to assemble file")
	}

	var copied int64
	for i := 0; i < file.ChunkCount; i++ {
		chunkPath := h.uploadChunkPath(session.ID, file.ID, i)
		in, err := os.Open(chunkPath)
		if err != nil {
			_ = out.Close()
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("Missing chunk %d for file %s", i, file.ID)
		}
		n, copyErr := io.Copy(out, in)
		_ = in.Close()
		if copyErr != nil {
			_ = out.Close()
			_ = os.Remove(tmpPath)
			return "", fmt.Errorf("Failed to assemble file")
		}
		copied += n
	}

	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("Failed to close assembled file")
	}
	if copied != file.Size {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("Assembled file size mismatch")
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("Failed to finalize assembled file")
	}
	_ = database.DB.Model(&models.UploadSessionFile{}).Where("id = ? AND upload_session_id = ?", file.ID, file.UploadSessionID).Update("assembled_path", finalPath).Error
	return finalPath, nil
}

func (h *Handler) uploadSessionRoot(sessionID string) string {
	return filepath.Join(h.config.TempDir, "resumable_uploads", sessionID)
}

func (h *Handler) uploadChunkPath(sessionID, fileID string, index int) string {
	return filepath.Join(h.uploadSessionRoot(sessionID), "chunks", fileID, fmt.Sprintf("%06d.part", index))
}

func newUploadToken() (string, string, error) {
	raw := make([]byte, uploadTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}

func uploadTokenMatches(token, tokenHash string) bool {
	sum := sha256.Sum256([]byte(token))
	actual := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(actual), []byte(tokenHash)) == 1
}

func parseChunkList(raw string) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return []int{}, nil
	}
	var chunks []int
	if err := json.Unmarshal([]byte(raw), &chunks); err != nil {
		return nil, err
	}
	sort.Ints(chunks)
	return chunks, nil
}

func missingChunks(total int, accepted []int) []int {
	seen := make(map[int]bool, len(accepted))
	for _, chunk := range accepted {
		seen[chunk] = true
	}
	missing := make([]int, 0)
	for i := 0; i < total; i++ {
		if !seen[i] {
			missing = append(missing, i)
		}
	}
	return missing
}

func chunkCount(size, chunkSize int64) int {
	if chunkSize <= 0 {
		return 0
	}
	return int((size + chunkSize - 1) / chunkSize)
}

func parseContentRange(value string) (int64, int64, int64, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, fmt.Errorf("Content-Range header is required")
	}
	value = strings.TrimPrefix(value, "bytes ")
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return 0, 0, 0, fmt.Errorf("Invalid Content-Range header")
	}
	rangeParts := strings.Split(parts[0], "-")
	if len(rangeParts) != 2 {
		return 0, 0, 0, fmt.Errorf("Invalid Content-Range header")
	}
	start, err := strconv.ParseInt(rangeParts[0], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("Invalid Content-Range start")
	}
	end, err := strconv.ParseInt(rangeParts[1], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("Invalid Content-Range end")
	}
	total, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("Invalid Content-Range total")
	}
	if start < 0 || end < start || total <= end {
		return 0, 0, 0, fmt.Errorf("Invalid Content-Range values")
	}
	return start, end, total, nil
}

func writeChunk(path string, body io.Reader, expectedSize int64, expectedHash string) error {
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("Failed to create chunk file")
	}
	defer out.Close()

	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(out, hasher), io.LimitReader(body, expectedSize+1))
	if err != nil {
		return fmt.Errorf("Failed to write chunk")
	}
	if written != expectedSize {
		return fmt.Errorf("Chunk body size does not match Content-Range")
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actualHash, expectedHash) {
		return fmt.Errorf("Chunk checksum mismatch")
	}
	return nil
}

func existingChunkMatches(path, expectedHash string, expectedSize int64) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Size() != expectedSize {
		return false, fmt.Errorf("Existing chunk has different size")
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return false, err
	}
	if !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), expectedHash) {
		return false, fmt.Errorf("Existing chunk checksum differs")
	}
	return true, nil
}

func validUploadFileID(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (h *Handler) cleanupExpiredUploadSessions() {
	var sessions []models.UploadSession
	if err := database.DB.Where("status = ? AND expires_at < ?", models.UploadSessionActive, time.Now()).Find(&sessions).Error; err != nil {
		return
	}
	for _, session := range sessions {
		_ = os.RemoveAll(h.uploadSessionRoot(session.ID))
		_ = database.DB.Model(&models.UploadSession{}).Where("id = ?", session.ID).Update("status", models.UploadSessionCancelled).Error
	}
}
