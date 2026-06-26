package tests

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"scriberr/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testUploadSessionResponse struct {
	ID        string                        `json:"id"`
	Kind      string                        `json:"kind"`
	Status    string                        `json:"status"`
	Token     string                        `json:"token"`
	ChunkSize int64                         `json:"chunk_size"`
	ResultID  *string                       `json:"result_id"`
	Files     []testUploadSessionFileStatus `json:"files"`
}

type testUploadSessionFileStatus struct {
	ID             string `json:"id"`
	Role           string `json:"role"`
	Name           string `json:"name"`
	Size           int64  `json:"size"`
	ChunkCount     int    `json:"chunk_count"`
	ReceivedBytes  int64  `json:"received_bytes"`
	AcceptedChunks []int  `json:"accepted_chunks"`
	MissingChunks  []int  `json:"missing_chunks"`
}

func (suite *APIHandlerTestSuite) TestResumableUploadCORSPreflight() {
	req, _ := http.NewRequest("OPTIONS", "/api/v1/transcription/uploads/session/files/audio/chunks/0", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Headers", "content-range,x-upload-token,x-chunk-sha256")

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusNoContent, w.Code)
	allowedHeaders := strings.ToLower(w.Header().Get("Access-Control-Allow-Headers"))
	assert.Contains(suite.T(), allowedHeaders, "content-range")
	assert.Contains(suite.T(), allowedHeaders, "x-upload-token")
	assert.Contains(suite.T(), allowedHeaders, "x-chunk-sha256")
}

func (suite *APIHandlerTestSuite) TestResumableUploadRejectsInvalidTokenAndChecksum() {
	data := []byte("hello-world")
	session := suite.createAudioUploadSession(data, "checksum.mp3")
	chunk := data[:session.ChunkSize]

	w := suite.putUploadChunk(session, "audio", 0, 0, chunk, "wrong-token", sha256HexForTest(chunk))
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)

	w = suite.putUploadChunk(session, "audio", 0, 0, chunk, session.Token, sha256HexForTest([]byte("different")))
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	status := suite.fetchUploadSession(session.ID)
	require.Len(suite.T(), status.Files, 1)
	assert.Empty(suite.T(), status.Files[0].AcceptedChunks)
	assert.Equal(suite.T(), int64(0), status.Files[0].ReceivedBytes)
}

func (suite *APIHandlerTestSuite) TestResumableUploadDuplicateChunkIsIdempotent() {
	data := []byte("hello-world")
	session := suite.createAudioUploadSession(data, "duplicate.mp3")
	chunk := data[:session.ChunkSize]
	hash := sha256HexForTest(chunk)

	w := suite.putUploadChunk(session, "audio", 0, 0, chunk, session.Token, hash)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	w = suite.putUploadChunk(session, "audio", 0, 0, chunk, session.Token, hash)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var duplicate map[string]bool
	require.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &duplicate))
	assert.True(suite.T(), duplicate["duplicate"])

	status := suite.fetchUploadSession(session.ID)
	require.Len(suite.T(), status.Files, 1)
	assert.Equal(suite.T(), []int{0}, status.Files[0].AcceptedChunks)
	assert.Equal(suite.T(), session.ChunkSize, status.Files[0].ReceivedBytes)
}

func (suite *APIHandlerTestSuite) TestResumableUploadMissingChunksCannotComplete() {
	data := []byte("hello-world")
	session := suite.createAudioUploadSession(data, "missing.mp3")
	chunk := data[:session.ChunkSize]

	w := suite.putUploadChunk(session, "audio", 0, 0, chunk, session.Token, sha256HexForTest(chunk))
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	w = suite.completeUploadSession(session)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Contains(suite.T(), w.Body.String(), "missing chunks")
}

func (suite *APIHandlerTestSuite) TestResumableAudioUploadCompletesAndRetriesComplete() {
	data := []byte("hello-world")
	session := suite.createAudioUploadSession(data, "complete.mp3")
	suite.uploadAllChunks(session, data)

	w := suite.completeUploadSession(session)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var job models.TranscriptionJob
	require.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &job))
	assert.NotEmpty(suite.T(), job.ID)
	assert.Equal(suite.T(), models.StatusUploaded, job.Status)
	assert.FileExists(suite.T(), job.AudioPath)

	w = suite.completeUploadSession(session)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var retried models.TranscriptionJob
	require.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &retried))
	assert.Equal(suite.T(), job.ID, retried.ID)
}

func (suite *APIHandlerTestSuite) TestResumableMultiTrackUploadPersistsAupAndTracks() {
	aupData := []byte(`<project audacityversion="2.4.2" datadir="project_data" rate="44100"><wavetrack name="Guitar" channel="0" linked="0" mute="0" solo="0" height="150" minimized="0" isSelected="0" rate="44100" gain="0.8" pan="-0.2"><waveclip offset="1.25"><import filename="Guitar One.wav" offset="0" channel="0"/></waveclip></wavetrack></project>`)
	trackData := []byte("track-audio")
	session := suite.createMultiTrackUploadSession(aupData, trackData)

	suite.uploadChunksForFile(session, "aup", aupData)
	suite.uploadChunksForFile(session, "track-0", trackData)

	w := suite.completeUploadSession(session)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	var job models.TranscriptionJob
	require.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &job))
	assert.True(suite.T(), job.IsMultiTrack)
	assert.Equal(suite.T(), models.StatusUploaded, job.Status)
	assert.Equal(suite.T(), "pending", job.MergeStatus)
	require.NotNil(suite.T(), job.AupFilePath)
	require.NotNil(suite.T(), job.MultiTrackFolder)
	assert.FileExists(suite.T(), *job.AupFilePath)
	assert.DirExists(suite.T(), *job.MultiTrackFolder)

	require.Len(suite.T(), job.MultiTrackFiles, 1)
	track := job.MultiTrackFiles[0]
	assert.Equal(suite.T(), "Guitar One.wav", track.FileName)
	assert.InDelta(suite.T(), 1.25, track.Offset, 0.001)
	assert.InDelta(suite.T(), 0.8, track.Gain, 0.001)
	assert.InDelta(suite.T(), -0.2, track.Pan, 0.001)
	assert.False(suite.T(), track.Mute)
	assert.FileExists(suite.T(), track.FilePath)
}

func (suite *APIHandlerTestSuite) TestResumableUploadCancelRemovesChunks() {
	data := []byte("hello-world")
	session := suite.createAudioUploadSession(data, "cancel.mp3")
	chunk := data[:session.ChunkSize]

	w := suite.putUploadChunk(session, "audio", 0, 0, chunk, session.Token, sha256HexForTest(chunk))
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	sessionRoot := filepath.Join(suite.helper.Config.TempDir, "resumable_uploads", session.ID)
	assert.DirExists(suite.T(), sessionRoot)

	req, _ := http.NewRequest("DELETE", fmt.Sprintf("/api/v1/transcription/uploads/%s", session.ID), nil)
	req.Header.Set("X-API-Key", suite.helper.TestAPIKey)
	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)
	assert.DirExists(suite.T(), sessionRoot)

	req, _ = http.NewRequest("DELETE", fmt.Sprintf("/api/v1/transcription/uploads/%s", session.ID), nil)
	req.Header.Set("X-API-Key", suite.helper.TestAPIKey)
	req.Header.Set("X-Upload-Token", session.Token)
	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), http.StatusOK, w.Code)
	_, err := os.Stat(sessionRoot)
	assert.True(suite.T(), os.IsNotExist(err), "session root should be removed after cancellation")
}

func (suite *APIHandlerTestSuite) TestResumableUploadRejectsHugeChunkPlan() {
	body := map[string]interface{}{
		"kind": "audio",
		"files": []map[string]interface{}{
			{
				"id":            "audio",
				"role":          "audio",
				"name":          "too-large.mp3",
				"content_type":  "audio/mpeg",
				"size":          suite.helper.Config.UploadChunkSizeBytes*10001 + 1,
				"last_modified": time.Now().UnixMilli(),
			},
		},
	}

	w := suite.makeAuthenticatedRequest("POST", "/api/v1/transcription/uploads", body, false)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Contains(suite.T(), w.Body.String(), "too many chunks")
}

func (suite *APIHandlerTestSuite) TestResumableUploadCreateCleansExpiredSessions() {
	expiredID := "expired-upload-session"
	sessionRoot := filepath.Join(suite.helper.Config.TempDir, "resumable_uploads", expiredID)
	require.NoError(suite.T(), os.MkdirAll(sessionRoot, 0755))

	expired := models.UploadSession{
		ID:        expiredID,
		Kind:      models.UploadKindAudio,
		Status:    models.UploadSessionActive,
		TokenHash: strings.Repeat("a", 64),
		ChunkSize: suite.helper.Config.UploadChunkSizeBytes,
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	require.NoError(suite.T(), suite.helper.DB.Create(&expired).Error)

	_ = suite.createAudioUploadSession([]byte("hello-world"), "cleanup.mp3")

	var reloaded models.UploadSession
	require.NoError(suite.T(), suite.helper.DB.Where("id = ?", expiredID).First(&reloaded).Error)
	assert.Equal(suite.T(), models.UploadSessionCancelled, reloaded.Status)
	_, err := os.Stat(sessionRoot)
	assert.True(suite.T(), os.IsNotExist(err), "expired session root should be removed")
}

func (suite *APIHandlerTestSuite) createAudioUploadSession(data []byte, filename string) testUploadSessionResponse {
	body := map[string]interface{}{
		"kind":  "audio",
		"title": strings.TrimSuffix(filename, filepath.Ext(filename)),
		"files": []map[string]interface{}{
			{
				"id":            "audio",
				"role":          "audio",
				"name":          filename,
				"content_type":  "audio/mpeg",
				"size":          len(data),
				"last_modified": time.Now().UnixMilli(),
			},
		},
	}

	w := suite.makeAuthenticatedRequest("POST", "/api/v1/transcription/uploads", body, false)
	require.Equal(suite.T(), http.StatusOK, w.Code, w.Body.String())

	var session testUploadSessionResponse
	require.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &session))
	require.NotEmpty(suite.T(), session.ID)
	require.NotEmpty(suite.T(), session.Token)
	require.Equal(suite.T(), suite.helper.Config.UploadChunkSizeBytes, session.ChunkSize)
	require.Len(suite.T(), session.Files, 1)
	return session
}

func (suite *APIHandlerTestSuite) createMultiTrackUploadSession(aupData, trackData []byte) testUploadSessionResponse {
	body := map[string]interface{}{
		"kind":  "multitrack",
		"title": "Chunked MultiTrack",
		"files": []map[string]interface{}{
			{
				"id":            "aup",
				"role":          "aup",
				"name":          "project.aup",
				"content_type":  "application/xml",
				"size":          len(aupData),
				"last_modified": time.Now().UnixMilli(),
			},
			{
				"id":            "track-0",
				"role":          "track",
				"name":          "Guitar One.wav",
				"content_type":  "audio/wav",
				"size":          len(trackData),
				"last_modified": time.Now().UnixMilli(),
			},
		},
	}

	w := suite.makeAuthenticatedRequest("POST", "/api/v1/transcription/uploads", body, false)
	require.Equal(suite.T(), http.StatusOK, w.Code, w.Body.String())

	var session testUploadSessionResponse
	require.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &session))
	require.NotEmpty(suite.T(), session.ID)
	require.NotEmpty(suite.T(), session.Token)
	require.Len(suite.T(), session.Files, 2)
	return session
}

func (suite *APIHandlerTestSuite) fetchUploadSession(sessionID string) testUploadSessionResponse {
	w := suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/transcription/uploads/%s", sessionID), nil, false)
	require.Equal(suite.T(), http.StatusOK, w.Code, w.Body.String())

	var session testUploadSessionResponse
	require.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &session))
	return session
}

func (suite *APIHandlerTestSuite) uploadAllChunks(session testUploadSessionResponse, data []byte) {
	suite.uploadChunksForFile(session, "audio", data)
}

func (suite *APIHandlerTestSuite) uploadChunksForFile(session testUploadSessionResponse, fileID string, data []byte) {
	fileStatus := uploadFileStatusByID(session, fileID)
	for index := 0; index < fileStatus.ChunkCount; index++ {
		start := int64(index) * session.ChunkSize
		end := start + session.ChunkSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		chunk := data[start:end]
		w := suite.putUploadChunk(session, fileID, index, start, chunk, session.Token, sha256HexForTest(chunk))
		require.Equal(suite.T(), http.StatusOK, w.Code, w.Body.String())
	}
}

func (suite *APIHandlerTestSuite) putUploadChunk(session testUploadSessionResponse, fileID string, index int, start int64, chunk []byte, token, hash string) *httptest.ResponseRecorder {
	fileStatus := uploadFileStatusByID(session, fileID)
	req, _ := http.NewRequest("PUT", fmt.Sprintf("/api/v1/transcription/uploads/%s/files/%s/chunks/%d", session.ID, fileID, index), bytes.NewReader(chunk))
	req.Header.Set("X-API-Key", suite.helper.TestAPIKey)
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+int64(len(chunk))-1, fileStatus.Size))
	req.Header.Set("X-Upload-Token", token)
	req.Header.Set("X-Chunk-SHA256", hash)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func uploadFileStatusByID(session testUploadSessionResponse, fileID string) testUploadSessionFileStatus {
	for _, file := range session.Files {
		if file.ID == fileID {
			return file
		}
	}
	return testUploadSessionFileStatus{}
}

func (suite *APIHandlerTestSuite) completeUploadSession(session testUploadSessionResponse) *httptest.ResponseRecorder {
	req, _ := http.NewRequest("POST", fmt.Sprintf("/api/v1/transcription/uploads/%s/complete", session.ID), nil)
	req.Header.Set("X-API-Key", suite.helper.TestAPIKey)
	req.Header.Set("X-Upload-Token", session.Token)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func sha256HexForTest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
