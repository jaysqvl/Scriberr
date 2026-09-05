package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"scriberr/internal/api"
	"scriberr/internal/models"
	"scriberr/internal/processing"
	"scriberr/internal/queue"
	"scriberr/internal/repository"
	"scriberr/internal/service"
	"scriberr/internal/sse"
	"scriberr/internal/transcription"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type APIHandlerTestSuite struct {
	suite.Suite
	helper             *TestHelper
	router             *gin.Engine
	handler            *api.Handler
	taskQueue          *queue.TaskQueue
	unifiedProcessor   *transcription.UnifiedJobProcessor
	quickTranscription *transcription.QuickTranscriptionService
	mockOpenAI         *httptest.Server
}

func (suite *APIHandlerTestSuite) SetupSuite() {
	suite.helper = NewTestHelper(suite.T(), "api_handlers_test.db")
	suite.mockOpenAI = NewMockOpenAIServer()

	// Initialize repositories
	jobRepo := repository.NewJobRepository(suite.helper.DB)
	userRepo := repository.NewUserRepository(suite.helper.DB)
	apiKeyRepo := repository.NewAPIKeyRepository(suite.helper.DB)
	profileRepo := repository.NewProfileRepository(suite.helper.DB)
	llmConfigRepo := repository.NewLLMConfigRepository(suite.helper.DB)
	summaryRepo := repository.NewSummaryRepository(suite.helper.DB)
	chatRepo := repository.NewChatRepository(suite.helper.DB)
	noteRepo := repository.NewNoteRepository(suite.helper.DB)
	speakerMappingRepo := repository.NewSpeakerMappingRepository(suite.helper.DB)
	refreshTokenRepo := repository.NewRefreshTokenRepository(suite.helper.DB)

	// Initialize services
	userService := service.NewUserService(userRepo, suite.helper.AuthService)
	fileService := service.NewFileService()

	// Initialize services
	suite.unifiedProcessor = transcription.NewUnifiedJobProcessor(jobRepo, suite.helper.Config.TempDir, suite.helper.Config.TranscriptsDir)
	var err error
	suite.quickTranscription, err = transcription.NewQuickTranscriptionService(suite.helper.Config, suite.unifiedProcessor, jobRepo)
	assert.NoError(suite.T(), err)

	suite.taskQueue = queue.NewTaskQueue(1, suite.unifiedProcessor, jobRepo)
	suite.taskQueue.SetTranscriptionQueueRepository(repository.NewTranscriptionQueueRepository(suite.helper.DB))

	broadcaster := sse.NewBroadcaster()

	multiTrackProcessor := processing.NewMultiTrackProcessor(suite.helper.DB, jobRepo)

	suite.handler = api.NewHandler(
		suite.helper.Config,
		suite.helper.AuthService,
		userService,
		fileService,
		jobRepo,
		apiKeyRepo,
		profileRepo,
		userRepo,
		llmConfigRepo,
		summaryRepo,
		chatRepo,
		noteRepo,
		speakerMappingRepo,
		refreshTokenRepo,
		suite.taskQueue,
		suite.unifiedProcessor,
		suite.quickTranscription,
		multiTrackProcessor,
		broadcaster,
	)
	suite.handler.SetOutboundHTTPClient(suite.mockOpenAI.Client())

	// Set up router
	suite.router = api.SetupRoutes(suite.handler, suite.helper.AuthService)
}

func (suite *APIHandlerTestSuite) TearDownSuite() {
	if suite.mockOpenAI != nil {
		suite.mockOpenAI.Close()
	}
	suite.helper.Cleanup()
}

func (suite *APIHandlerTestSuite) SetupTest() {
	suite.helper.ResetDB(suite.T())

	// Create LLM config pointing to mock server
	llmConfig := &models.LLMConfig{
		Provider:      "openai",
		OpenAIBaseURL: &suite.mockOpenAI.URL,
		APIKey:        stringPtr("test-api-key"),
		IsActive:      true,
	}
	err := suite.helper.DB.Create(llmConfig).Error
	assert.NoError(suite.T(), err)
}

// Helper method to make authenticated requests
func (suite *APIHandlerTestSuite) makeAuthenticatedRequest(method, path string, body interface{}, useJWT bool) *httptest.ResponseRecorder {
	var req *http.Request
	var err error

	if body != nil {
		switch v := body.(type) {
		case string:
			req, err = http.NewRequest(method, path, strings.NewReader(v))
		case []byte:
			req, err = http.NewRequest(method, path, bytes.NewBuffer(v))
		case *bytes.Buffer:
			req, err = http.NewRequest(method, path, v)
		default:
			jsonBody, _ := json.Marshal(v)
			req, err = http.NewRequest(method, path, bytes.NewBuffer(jsonBody))
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		req, err = http.NewRequest(method, path, nil)
	}

	assert.NoError(suite.T(), err)

	// Add authentication
	if useJWT {
		req.Header.Set("Authorization", "Bearer "+suite.helper.TestToken)
	} else {
		req.Header.Set("X-API-Key", suite.helper.TestAPIKey)
	}

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func (suite *APIHandlerTestSuite) makeLoginRequest(router *gin.Engine, username, password, remoteAddr, forwardedFor string) *httptest.ResponseRecorder {
	loginData := map[string]string{
		"username": username,
		"password": password,
	}

	jsonData, _ := json.Marshal(loginData)
	req, _ := http.NewRequest("POST", "/api/v1/auth/login", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// Test health check endpoint
func (suite *APIHandlerTestSuite) TestHealthCheck() {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health", nil)
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), 200, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "healthy", response["status"])
	assert.Contains(suite.T(), response, "version")
	assert.Contains(suite.T(), response, "commit")
	assert.Contains(suite.T(), response, "built")
}

func (suite *APIHandlerTestSuite) TestDownloadFromYouTubeRejectsSSRFURLs() {
	badURLs := []string{
		"https://youtube.com.evil.example/watch?v=abc123",
		"https://evil.example/watch?next=youtube.com",
		"https://youtube.com@127.0.0.1/watch?v=abc123",
		"https://169.254.169.254/latest/meta-data?youtube.com",
		"http://youtube.com/watch?v=abc123",
	}

	for _, rawURL := range badURLs {
		suite.T().Run(rawURL, func(t *testing.T) {
			w := suite.makeAuthenticatedRequest("POST", "/api/v1/transcription/youtube", map[string]string{"url": rawURL}, false)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "Invalid YouTube URL")
		})
	}
}

// Test user registration
func (suite *APIHandlerTestSuite) TestRegisterUser() {
	registerData := map[string]string{
		"username": "newuser123",
		"password": "newpassword123",
	}

	jsonData, _ := json.Marshal(registerData)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/auth/register", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	suite.router.ServeHTTP(w, req)

	// Should return 400 because registration might be disabled or user already exists
	assert.True(suite.T(), w.Code == 200 || w.Code == 400 || w.Code == 409)
}

// Test user login
func (suite *APIHandlerTestSuite) TestLoginUser() {
	loginData := map[string]string{
		"username": suite.helper.TestUser.Username,
		"password": "testpassword123",
	}

	jsonData, _ := json.Marshal(loginData)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/auth/login", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), 200, w.Code)

	var response api.LoginResponse
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), response.Token)
	assert.Equal(suite.T(), suite.helper.TestUser.Username, response.User.Username)
}

func (suite *APIHandlerTestSuite) TestHTTPLoginCookieStreamsAuthenticatedAudioRange() {
	originalMode := suite.helper.Config.SecureCookiesMode
	suite.helper.Config.SecureCookiesMode = "auto"
	defer func() {
		suite.helper.Config.SecureCookiesMode = originalMode
	}()

	loginData := map[string]string{
		"username": suite.helper.TestUser.Username,
		"password": "testpassword123",
	}
	jsonData, err := json.Marshal(loginData)
	require.NoError(suite.T(), err)

	loginRequest := httptest.NewRequest(http.MethodPost, "http://scriberr.test/api/v1/auth/login", bytes.NewReader(jsonData))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	suite.router.ServeHTTP(loginResponse, loginRequest)
	require.Equal(suite.T(), http.StatusOK, loginResponse.Code)

	var accessCookie *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		if cookie.Name == "scriberr_access_token" {
			accessCookie = cookie
			break
		}
	}
	require.NotNil(suite.T(), accessCookie)
	require.False(suite.T(), accessCookie.Secure, "plain-HTTP sessions must receive a cookie the browser can send to <audio>")
	require.True(suite.T(), accessCookie.HttpOnly)

	audioBytes := []byte("ID3abcdef")
	audioPath := suite.T().TempDir() + "/existing recording.mp3"
	require.NoError(suite.T(), os.WriteFile(audioPath, audioBytes, 0600))
	job := suite.helper.CreateTestTranscriptionJob(suite.T(), "Existing recording")
	job.AudioPath = audioPath
	require.NoError(suite.T(), suite.helper.DB.Save(job).Error)

	streamRequest := httptest.NewRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/audio", nil)
	streamRequest.AddCookie(accessCookie)
	streamRequest.Header.Set("Range", "bytes=3-6")
	streamResponse := httptest.NewRecorder()
	suite.router.ServeHTTP(streamResponse, streamRequest)

	require.Equal(suite.T(), http.StatusPartialContent, streamResponse.Code)
	require.Equal(suite.T(), "bytes", streamResponse.Header().Get("Accept-Ranges"))
	require.Equal(suite.T(), "bytes 3-6/9", streamResponse.Header().Get("Content-Range"))
	require.Equal(suite.T(), audioBytes[3:7], streamResponse.Body.Bytes())
}

func (suite *APIHandlerTestSuite) TestPersistedBearerSessionRepairsAudioCookie() {
	originalMode := suite.helper.Config.SecureCookiesMode
	suite.helper.Config.SecureCookiesMode = "auto"
	defer func() {
		suite.helper.Config.SecureCookiesMode = originalMode
	}()

	audioBytes := []byte("ID3persisted-session")
	audioPath := suite.T().TempDir() + "/persisted session.mp3"
	require.NoError(suite.T(), os.WriteFile(audioPath, audioBytes, 0600))
	job := suite.helper.CreateTestTranscriptionJob(suite.T(), "Persisted session")
	job.AudioPath = audioPath
	require.NoError(suite.T(), suite.helper.DB.Save(job).Error)

	// This is the first JSON request after restoring a JWT from localStorage.
	// It must repair the cookie before React mounts the native <audio> element.
	detailRequest := httptest.NewRequest(http.MethodGet, "/api/v1/transcription/"+job.ID, nil)
	detailRequest.Header.Set("Authorization", "Bearer "+suite.helper.TestToken)
	detailResponse := httptest.NewRecorder()
	suite.router.ServeHTTP(detailResponse, detailRequest)
	require.Equal(suite.T(), http.StatusOK, detailResponse.Code)

	var accessCookie *http.Cookie
	for _, cookie := range detailResponse.Result().Cookies() {
		if cookie.Name == "scriberr_access_token" {
			accessCookie = cookie
			break
		}
	}
	require.NotNil(suite.T(), accessCookie, "a validated bearer session must be synchronized for native media requests")
	require.False(suite.T(), accessCookie.Secure)
	require.Equal(suite.T(), suite.helper.TestToken, accessCookie.Value)

	streamRequest := httptest.NewRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/audio", nil)
	streamRequest.AddCookie(accessCookie)
	streamRequest.Header.Set("Range", "bytes=0-2")
	streamResponse := httptest.NewRecorder()
	suite.router.ServeHTTP(streamResponse, streamRequest)

	require.Equal(suite.T(), http.StatusPartialContent, streamResponse.Code)
	require.Equal(suite.T(), "bytes 0-2/20", streamResponse.Header().Get("Content-Range"))
	require.Equal(suite.T(), []byte("ID3"), streamResponse.Body.Bytes())
}

func (suite *APIHandlerTestSuite) TestRegistrationSetsMediaAccessCookie() {
	originalMode := suite.helper.Config.SecureCookiesMode
	suite.helper.Config.SecureCookiesMode = "auto"
	defer func() {
		suite.helper.Config.SecureCookiesMode = originalMode
	}()

	require.NoError(suite.T(), suite.helper.DB.Where("id = ?", suite.helper.TestUser.ID).Delete(&models.User{}).Error)
	registerData := map[string]string{
		"username":        "first-admin",
		"password":        "testpassword123",
		"confirmPassword": "testpassword123",
	}
	jsonData, err := json.Marshal(registerData)
	require.NoError(suite.T(), err)

	request := httptest.NewRequest(http.MethodPost, "http://scriberr.test/api/v1/auth/register", bytes.NewReader(jsonData))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	suite.router.ServeHTTP(response, request)
	require.Equal(suite.T(), http.StatusCreated, response.Code)

	var accessCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "scriberr_access_token" {
			accessCookie = cookie
			break
		}
	}
	require.NotNil(suite.T(), accessCookie, "registration logs the user in and must authorize native media")
	require.False(suite.T(), accessCookie.Secure)
	require.True(suite.T(), accessCookie.HttpOnly)
}

func (suite *APIHandlerTestSuite) TestLoginRateLimitInvalidCredentials() {
	w := suite.makeLoginRequest(suite.router, suite.helper.TestUser.Username, "wrong-password", "198.51.100.10:1000", "")
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)

	w = suite.makeLoginRequest(suite.router, suite.helper.TestUser.Username, "wrong-password", "198.51.100.10:1001", "")
	assert.Equal(suite.T(), http.StatusTooManyRequests, w.Code)
	assert.NotEmpty(suite.T(), w.Header().Get("Retry-After"))

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Too many login attempts. Try again later.", response["error"])
}

func (suite *APIHandlerTestSuite) TestLoginRateLimitUnknownUserMatchesBadPassword() {
	w := suite.makeLoginRequest(suite.router, "missing-rate-limit-user", "wrong-password", "198.51.100.11:1000", "")
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)

	w = suite.makeLoginRequest(suite.router, "missing-rate-limit-user", "wrong-password", "198.51.100.11:1001", "")
	assert.Equal(suite.T(), http.StatusTooManyRequests, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Too many login attempts. Try again later.", response["error"])
}

func (suite *APIHandlerTestSuite) TestLoginRateLimitIgnoresForwardedForWithoutTrustedProxy() {
	w := suite.makeLoginRequest(suite.router, "proxy-untrusted-user", "wrong-password", "192.0.2.200:1000", "203.0.113.10")
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)

	w = suite.makeLoginRequest(suite.router, "proxy-untrusted-user", "wrong-password", "192.0.2.200:1001", "203.0.113.11")
	assert.Equal(suite.T(), http.StatusTooManyRequests, w.Code)
}

func (suite *APIHandlerTestSuite) TestLoginRateLimitHonorsTrustedProxy() {
	originalTrustedProxies := suite.helper.Config.TrustedProxies
	suite.helper.Config.TrustedProxies = []string{"192.0.2.210"}
	defer func() {
		suite.helper.Config.TrustedProxies = originalTrustedProxies
	}()

	router := api.SetupRoutes(suite.handler, suite.helper.AuthService)

	w := suite.makeLoginRequest(router, "proxy-trusted-user", "wrong-password", "192.0.2.210:1000", "203.0.113.10")
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)

	w = suite.makeLoginRequest(router, "proxy-trusted-user", "wrong-password", "192.0.2.210:1001", "203.0.113.11")
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)
}

// Test getting registration status
func (suite *APIHandlerTestSuite) TestGetRegistrationStatus() {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/auth/registration-status", nil)
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), 200, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Contains(suite.T(), response, "registration_enabled")
}

// Test API key management
func (suite *APIHandlerTestSuite) TestAPIKeyManagement() {
	// List API keys (JWT required)
	w := suite.makeAuthenticatedRequest("GET", "/api/v1/api-keys/", nil, true)
	assert.Equal(suite.T(), 200, w.Code)

	var wrappedResponse struct {
		APIKeys []struct {
			ID          uint   `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			KeyPreview  string `json:"key_preview"`
			IsActive    bool   `json:"is_active"`
			CreatedAt   string `json:"created_at"`
			UpdatedAt   string `json:"updated_at"`
			LastUsed    string `json:"last_used"`
		} `json:"api_keys"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &wrappedResponse)
	assert.NoError(suite.T(), err)

	// Should contain at least our test API key (check by key preview)
	found := false
	testKeyPreview := suite.helper.TestAPIKey[:8] + "..."
	for _, key := range wrappedResponse.APIKeys {
		if key.KeyPreview == testKeyPreview {
			found = true
			break
		}
	}
	assert.True(suite.T(), found)

	// Create new API key (JWT required)
	createData := map[string]string{
		"name":        "Test Created Key",
		"description": "Key created during testing",
	}

	w = suite.makeAuthenticatedRequest("POST", "/api/v1/api-keys/", createData, true)
	assert.Equal(suite.T(), 200, w.Code)

	var createResponse models.APIKey
	err = json.Unmarshal(w.Body.Bytes(), &createResponse)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Test Created Key", createResponse.Name)
	assert.NotEmpty(suite.T(), createResponse.Key)

	// Delete the created API key
	w = suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/api-keys/%d", createResponse.ID), nil, true)
	assert.Equal(suite.T(), 200, w.Code)
}

// Test transcription job listing
func (suite *APIHandlerTestSuite) TestListTranscriptionJobs() {
	// Create a test job first
	testJob := suite.helper.CreateTestTranscriptionJob(suite.T(), "Test Job for Listing")

	w := suite.makeAuthenticatedRequest("GET", "/api/v1/transcription/list", nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)

	assert.Contains(suite.T(), response, "jobs")
	assert.Contains(suite.T(), response, "pagination")

	jobs := response["jobs"].([]interface{})
	assert.GreaterOrEqual(suite.T(), len(jobs), 1)

	// Check if our test job is in the list
	foundJob := false
	for _, job := range jobs {
		jobMap := job.(map[string]interface{})
		if jobMap["id"] == testJob.ID {
			foundJob = true
			break
		}
	}
	assert.True(suite.T(), foundJob)
}

// Test transcription job listing with delta sync
func (suite *APIHandlerTestSuite) TestListTranscriptionJobsDeltaSync() {
	// 1. Create a job
	job1 := suite.helper.CreateTestTranscriptionJob(suite.T(), "Job 1 (Active)")
	time.Sleep(10 * time.Millisecond) // Ensure unique timestamp

	// 2. Create another job
	job2 := suite.helper.CreateTestTranscriptionJob(suite.T(), "Job 2 (To Be Deleted)")
	require.NoError(suite.T(), suite.helper.DB.Model(job2).Update("status", models.StatusCompleted).Error)
	time.Sleep(10 * time.Millisecond)

	// Capture time before deletion (but after creation)
	syncTime := time.Now().Add(-5 * time.Second) // Set sync time to slightly before now to pick up these jobs if they updated?
	// Actually, we want to test:
	// - created job is returned
	// - deleted job is returned if updated_after < deletion_time

	// Let's delete job2
	w := suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/transcription/%s", job2.ID), nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	// Case A: Normal List (No param) -> Should return job1, NOT job2
	w = suite.makeAuthenticatedRequest("GET", "/api/v1/transcription/list", nil, false)
	assert.Equal(suite.T(), 200, w.Code)
	var responseStandard map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &responseStandard)
	jobsStd := responseStandard["jobs"].([]interface{})

	foundJob1 := false
	foundJob2 := false
	for _, j := range jobsStd {
		jm := j.(map[string]interface{})
		if jm["id"] == job1.ID {
			foundJob1 = true
		}
		if jm["id"] == job2.ID {
			foundJob2 = true
		}
	}
	assert.True(suite.T(), foundJob1, "Active job should be found in standard list")
	assert.False(suite.T(), foundJob2, "Deleted job should NOT be found in standard list")

	// Case B: Delta Sync (updated_after)
	// We want to see both jobs because both were updated (created or deleted) recently.
	updatedAfter := syncTime.Format(time.RFC3339)
	w = suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/transcription/list?updated_after=%s", updatedAfter), nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	var responseDelta map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &responseDelta)
	jobsDelta := responseDelta["jobs"].([]interface{})

	foundJob1 = false
	foundJob2 = false
	var job2Data map[string]interface{}

	for _, j := range jobsDelta {
		jm := j.(map[string]interface{})
		if jm["id"] == job1.ID {
			foundJob1 = true
		}
		if jm["id"] == job2.ID {
			foundJob2 = true
			job2Data = jm
		}
	}
	assert.True(suite.T(), foundJob1, "Active job should be found in delta sync")
	assert.True(suite.T(), foundJob2, "Deleted job SHOULD be found in delta sync")

	// Verify deleted_at is set for job2
	if job2Data != nil {
		_, hasDeletedAt := job2Data["deleted_at"]
		// deleted_at might be nil or string
		assert.True(suite.T(), hasDeletedAt, "deleted_at field should be present")
		assert.NotNil(suite.T(), job2Data["deleted_at"], "deleted_at should not be nil for deleted job")
	}
}

// Test getting transcription job by ID
func (suite *APIHandlerTestSuite) TestGetTranscriptionJobByID() {
	testJob := suite.helper.CreateTestTranscriptionJob(suite.T(), "Test Job by ID")

	w := suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/transcription/%s", testJob.ID), nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	var response models.TranscriptionJob
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), testJob.ID, response.ID)
	assert.Equal(suite.T(), *testJob.Title, *response.Title)
}

// Test getting job status
func (suite *APIHandlerTestSuite) TestGetJobStatus() {
	testJob := suite.helper.CreateTestTranscriptionJob(suite.T(), "Test Job Status")

	w := suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/transcription/%s/status", testJob.ID), nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	var response models.TranscriptionJob
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), testJob.ID, response.ID)
	assert.Equal(suite.T(), models.StatusPending, response.Status)
}

// Test updating transcription title
func (suite *APIHandlerTestSuite) TestUpdateTranscriptionTitle() {
	testJob := suite.helper.CreateTestTranscriptionJob(suite.T(), "Original Title")

	updateData := map[string]string{
		"title": "Updated Title",
	}

	w := suite.makeAuthenticatedRequest("PUT", fmt.Sprintf("/api/v1/transcription/%s/title", testJob.ID), updateData, false)
	assert.Equal(suite.T(), 200, w.Code)

	// Verify the title was updated
	w = suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/transcription/%s", testJob.ID), nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	var response models.TranscriptionJob
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Updated Title", *response.Title)
}

// Test deleting transcription job
func (suite *APIHandlerTestSuite) TestDeleteTranscriptionJob() {
	testJob := suite.helper.CreateTestTranscriptionJob(suite.T(), "Job to Delete")
	require.NoError(suite.T(), suite.helper.DB.Model(testJob).Update("status", models.StatusCompleted).Error)

	w := suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/transcription/%s", testJob.ID), nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	// Verify the job was deleted
	w = suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/transcription/%s", testJob.ID), nil, false)
	assert.Equal(suite.T(), 404, w.Code)
}

func (suite *APIHandlerTestSuite) TestDeleteTranscriptionJobRejectsPendingWork() {
	testJob := suite.helper.CreateTestTranscriptionJob(suite.T(), "Pending Job")

	w := suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/transcription/%s", testJob.ID), nil, false)
	assert.Equal(suite.T(), http.StatusConflict, w.Code)

	var count int64
	require.NoError(suite.T(), suite.helper.DB.Model(&models.TranscriptionJob{}).
		Where("id = ?", testJob.ID).Count(&count).Error)
	assert.Equal(suite.T(), int64(1), count)
}

// Test getting supported models
func (suite *APIHandlerTestSuite) TestGetSupportedModels() {
	w := suite.makeAuthenticatedRequest("GET", "/api/v1/transcription/models", nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)

	assert.Contains(suite.T(), response, "models")
	assert.Contains(suite.T(), response, "languages")

	// Models is now a map (model_id -> capabilities), languages is still an array
	// In test environment, these may be empty since no adapters are registered
	models := response["models"].(map[string]interface{})
	languages := response["languages"].([]interface{})

	// Just verify they have the correct types (may be empty in test environment)
	assert.NotNil(suite.T(), models)
	assert.NotNil(suite.T(), languages)
}

// Test profile management
func (suite *APIHandlerTestSuite) TestProfileManagement() {
	// List profiles
	w := suite.makeAuthenticatedRequest("GET", "/api/v1/profiles/", nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	// Create profile
	profileData := map[string]interface{}{
		"name":        "Test Profile",
		"description": "Test profile description",
		"parameters": map[string]interface{}{
			"model":      "base",
			"batch_size": 16,
			"device":     "auto",
		},
	}

	w = suite.makeAuthenticatedRequest("POST", "/api/v1/profiles/", profileData, false)
	assert.Equal(suite.T(), 200, w.Code)

	var createResponse models.TranscriptionProfile
	err := json.Unmarshal(w.Body.Bytes(), &createResponse)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Test Profile", createResponse.Name)

	// Get profile
	w = suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/profiles/%s", createResponse.ID), nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	// Update profile
	updateData := map[string]interface{}{
		"name":        "Updated Profile",
		"description": "Updated description",
	}

	w = suite.makeAuthenticatedRequest("PUT", fmt.Sprintf("/api/v1/profiles/%s", createResponse.ID), updateData, false)
	assert.Equal(suite.T(), 200, w.Code)

	// Delete profile
	w = suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/profiles/%s", createResponse.ID), nil, false)
	assert.Equal(suite.T(), 200, w.Code)
}

// Test notes management
func (suite *APIHandlerTestSuite) TestNotesManagement() {
	// Create a transcription job first
	testJob := suite.helper.CreateTestTranscriptionJob(suite.T(), "Job for Notes")

	// Create note
	noteData := map[string]interface{}{
		"start_word_index": 0,
		"end_word_index":   5,
		"start_time":       0.0,
		"end_time":         2.5,
		"quote":            "Test quote text",
		"content":          "Test note content",
	}

	w := suite.makeAuthenticatedRequest("POST", fmt.Sprintf("/api/v1/transcription/%s/notes", testJob.ID), noteData, false)
	assert.Equal(suite.T(), 200, w.Code)

	var createResponse models.Note
	err := json.Unmarshal(w.Body.Bytes(), &createResponse)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Test note content", createResponse.Content)

	// List notes for transcription
	w = suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/transcription/%s/notes", testJob.ID), nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	var listResponse []models.Note
	err = json.Unmarshal(w.Body.Bytes(), &listResponse)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(listResponse), 1)

	// Update note
	updateData := map[string]string{
		"content": "Updated note content",
	}

	w = suite.makeAuthenticatedRequest("PUT", fmt.Sprintf("/api/v1/notes/%s", createResponse.ID), updateData, false)
	assert.Equal(suite.T(), 200, w.Code)

	// Get updated note
	w = suite.makeAuthenticatedRequest("GET", fmt.Sprintf("/api/v1/notes/%s", createResponse.ID), nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	var updatedNote models.Note
	err = json.Unmarshal(w.Body.Bytes(), &updatedNote)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Updated note content", updatedNote.Content)

	// Delete note
	w = suite.makeAuthenticatedRequest("DELETE", fmt.Sprintf("/api/v1/notes/%s", createResponse.ID), nil, false)
	assert.Equal(suite.T(), 200, w.Code)
}

// Test queue stats
func (suite *APIHandlerTestSuite) TestGetQueueStats() {
	w := suite.makeAuthenticatedRequest("GET", "/api/v1/admin/queue/stats", nil, false)
	assert.Equal(suite.T(), 200, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)

	assert.Contains(suite.T(), response, "queue_size")
	assert.Contains(suite.T(), response, "current_workers")
	assert.Contains(suite.T(), response, "pending_jobs")
	assert.Contains(suite.T(), response, "processing_jobs")
	assert.Contains(suite.T(), response, "completed_jobs")
	assert.Contains(suite.T(), response, "failed_jobs")
}

// Test multipart file upload (transcription submit)
func (suite *APIHandlerTestSuite) TestTranscriptionSubmit() {
	// Create a dummy audio file
	tmpFile, err := os.CreateTemp("", "test_audio_*.mp3")
	assert.NoError(suite.T(), err)
	defer os.Remove(tmpFile.Name())

	tmpFile.WriteString("dummy audio data for API handler testing")
	tmpFile.Close()

	// Create multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add audio file
	file, err := os.Open(tmpFile.Name())
	assert.NoError(suite.T(), err)
	defer file.Close()

	part, err := writer.CreateFormFile("audio", "test.mp3")
	assert.NoError(suite.T(), err)
	io.Copy(part, file)

	// Add form fields
	writer.WriteField("title", "API Handler Test Audio")
	writer.WriteField("model", "base")
	writer.WriteField("diarization", "false")

	writer.Close()

	req, _ := http.NewRequest("POST", "/api/v1/transcription/submit", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-API-Key", suite.helper.TestAPIKey)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), 200, w.Code)

	var response models.TranscriptionJob
	err = json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), response.ID)
	assert.Equal(suite.T(), "API Handler Test Audio", *response.Title)
	assert.Equal(suite.T(), models.StatusPending, response.Status)
}

// Test error responses for non-existent resources
func (suite *APIHandlerTestSuite) TestNotFoundErrors() {
	endpoints := []string{
		"/api/v1/transcription/nonexistent-job",
		"/api/v1/transcription/nonexistent-job/status",
		"/api/v1/transcription/nonexistent-job/transcript",
		"/api/v1/profiles/nonexistent-profile",
		"/api/v1/notes/nonexistent-note",
	}

	for _, endpoint := range endpoints {
		w := suite.makeAuthenticatedRequest("GET", endpoint, nil, false)
		assert.Equal(suite.T(), 404, w.Code, "Endpoint %s should return 404", endpoint)
	}
}

// Test invalid request data
func (suite *APIHandlerTestSuite) TestInvalidRequestData() {
	// Test invalid JSON for login
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/auth/login", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), 400, w.Code)

	// Test missing required fields
	emptyLogin := map[string]string{}
	w = suite.makeAuthenticatedRequest("POST", "/api/v1/auth/login", emptyLogin, false)
	assert.True(suite.T(), w.Code >= 400, "Should return error for empty login data")
}

// Test logout
func (suite *APIHandlerTestSuite) TestLogout() {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/auth/logout", nil)
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), 200, w.Code)
}

func (suite *APIHandlerTestSuite) TestLogoutRevokesPresentedAccessToken() {
	w := suite.makeAuthenticatedRequest("GET", "/api/v1/transcription/list", nil, true)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	req, _ := http.NewRequest("POST", "/api/v1/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+suite.helper.TestToken)
	w = httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	assert.Equal(suite.T(), http.StatusOK, w.Code)

	w = suite.makeAuthenticatedRequest("GET", "/api/v1/transcription/list", nil, true)
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)
}

func (suite *APIHandlerTestSuite) TestPasswordChangeRevokesAccessRefreshAndCLITokens() {
	cliToken, err := suite.helper.AuthService.GenerateLongLivedToken(suite.helper.TestUser)
	assert.NoError(suite.T(), err)

	login := suite.makeLoginRequest(suite.router, suite.helper.TestUser.Username, "testpassword123", "203.0.113.250:5000", "")
	assert.Equal(suite.T(), http.StatusOK, login.Code)
	var refreshCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == "scriberr_refresh_token" {
			refreshCookie = cookie
			break
		}
	}
	assert.NotNil(suite.T(), refreshCookie)

	change := suite.makeAuthenticatedRequest("POST", "/api/v1/auth/change-password", map[string]string{
		"currentPassword": "testpassword123",
		"newPassword":     "new-test-password",
		"confirmPassword": "new-test-password",
	}, true)
	assert.Equal(suite.T(), http.StatusOK, change.Code, change.Body.String())

	for _, token := range []string{suite.helper.TestToken, cliToken} {
		req, _ := http.NewRequest("GET", "/api/v1/transcription/list", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		suite.router.ServeHTTP(w, req)
		assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)
	}

	refreshReq, _ := http.NewRequest("POST", "/api/v1/auth/refresh", nil)
	refreshReq.AddCookie(refreshCookie)
	refresh := httptest.NewRecorder()
	suite.router.ServeHTTP(refresh, refreshReq)
	assert.Equal(suite.T(), http.StatusUnauthorized, refresh.Code)
}

func (suite *APIHandlerTestSuite) TestAPIKeyCannotAdministerLLMConfiguration() {
	w := suite.makeAuthenticatedRequest("GET", "/api/v1/llm/config", nil, false)
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)

	w = suite.makeAuthenticatedRequest("POST", "/api/v1/llm/config", map[string]interface{}{
		"provider":        "openai",
		"openai_base_url": "https://attacker.example/v1",
		"is_active":       true,
	}, false)
	assert.Equal(suite.T(), http.StatusUnauthorized, w.Code)
}

func (suite *APIHandlerTestSuite) TestLLMConfigurationRejectsProtectedOriginsAndSecretReuseAcrossOrigins() {
	w := suite.makeAuthenticatedRequest("POST", "/api/v1/llm/config", map[string]interface{}{
		"provider":        "openai",
		"openai_base_url": "https://127.0.0.1/v1",
		"api_key":         "replacement-key",
		"is_active":       true,
	}, true)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)

	w = suite.makeAuthenticatedRequest("POST", "/api/v1/llm/config", map[string]interface{}{
		"provider":        "openai",
		"openai_base_url": "https://example.com/v1",
		"is_active":       true,
	}, true)
	assert.Equal(suite.T(), http.StatusBadRequest, w.Code)
	assert.Contains(suite.T(), w.Body.String(), "API key is required")
}

func (suite *APIHandlerTestSuite) TestProfileSecretsArePreservedButNeverReturned() {
	hfToken := "hf_profile_secret"
	apiKey := "sk_profile_secret"
	w := suite.makeAuthenticatedRequest("POST", "/api/v1/profiles/", map[string]interface{}{
		"name": "Secret Profile",
		"parameters": map[string]interface{}{
			"model":    "small",
			"hf_token": hfToken,
			"api_key":  apiKey,
		},
	}, false)
	assert.Equal(suite.T(), http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(suite.T(), w.Body.String(), hfToken)
	assert.NotContains(suite.T(), w.Body.String(), apiKey)

	var created models.TranscriptionProfile
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &created))
	w = suite.makeAuthenticatedRequest("PUT", "/api/v1/profiles/"+created.ID, map[string]interface{}{
		"name": "Renamed Secret Profile",
	}, false)
	assert.Equal(suite.T(), http.StatusOK, w.Code, w.Body.String())
	assert.NotContains(suite.T(), w.Body.String(), hfToken)
	assert.NotContains(suite.T(), w.Body.String(), apiKey)

	var stored models.TranscriptionProfile
	assert.NoError(suite.T(), suite.helper.DB.Where("id = ?", created.ID).First(&stored).Error)
	assert.NotNil(suite.T(), stored.Parameters.HfToken)
	assert.NotNil(suite.T(), stored.Parameters.APIKey)
	assert.Equal(suite.T(), hfToken, *stored.Parameters.HfToken)
	assert.Equal(suite.T(), apiKey, *stored.Parameters.APIKey)
}

func (suite *APIHandlerTestSuite) TestOversizedLoginBodyReturnsRequestTooLarge() {
	body := `{"username":"` + strings.Repeat("a", 70*1024) + `","password":"test"}`
	req, _ := http.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	assert.Equal(suite.T(), http.StatusRequestEntityTooLarge, w.Code)
}

func TestAPIHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(APIHandlerTestSuite))
}
