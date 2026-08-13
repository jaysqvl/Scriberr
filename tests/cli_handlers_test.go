package tests

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"scriberr/internal/api"
	"scriberr/internal/processing"
	"scriberr/internal/queue"
	"scriberr/internal/repository"
	"scriberr/internal/service"
	"scriberr/internal/sse"
	"scriberr/internal/transcription"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type CLIHandlerTestSuite struct {
	suite.Suite
	helper             *TestHelper
	router             *gin.Engine
	handler            *api.Handler
	taskQueue          *queue.TaskQueue
	unifiedProcessor   *transcription.UnifiedJobProcessor
	quickTranscription *transcription.QuickTranscriptionService
}

func (suite *CLIHandlerTestSuite) SetupSuite() {
	suite.helper = NewTestHelper(suite.T(), "cli_handlers_test.db")

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

	// Set up router
	suite.router = api.SetupRoutes(suite.handler, suite.helper.AuthService)
}

func (suite *CLIHandlerTestSuite) TearDownSuite() {
	suite.helper.Cleanup()
}

func (suite *CLIHandlerTestSuite) SetupTest() {
	suite.helper.ResetDB(suite.T())
}

func (suite *CLIHandlerTestSuite) makeAuthenticatedRequest(method, path string, body interface{}) *httptest.ResponseRecorder {
	var req *http.Request
	var err error

	if body != nil {
		jsonBody, _ := json.Marshal(body)
		req, err = http.NewRequest(method, path, strings.NewReader(string(jsonBody)))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(method, path, nil)
	}

	assert.NoError(suite.T(), err)
	req.Header.Set("Authorization", "Bearer "+suite.helper.TestToken)

	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	return w
}

func (suite *CLIHandlerTestSuite) TestAuthorizeCLI() {
	state, _ := suite.startCLIAuthorization("http://127.0.0.1:12345/callback")
	// Test GET /api/v1/auth/cli/authorize
	w := suite.makeAuthenticatedRequest("GET", "/api/v1/auth/cli/authorize?state="+url.QueryEscape(state), nil)
	assert.Equal(suite.T(), 200, w.Code)
	assert.Equal(suite.T(), "DENY", w.Header().Get("X-Frame-Options"))
	assert.Contains(suite.T(), w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'")

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)

	userMap := response["user"].(map[string]interface{})
	assert.Equal(suite.T(), float64(suite.helper.TestUser.ID), userMap["id"])
	assert.Equal(suite.T(), suite.helper.TestUser.Username, userMap["username"])
}

func (suite *CLIHandlerTestSuite) TestConfirmCLIAuthorization() {
	state, verifier := suite.startCLIAuthorization("http://127.0.0.1:12345/callback")

	w := suite.makeAuthenticatedRequest("POST", "/api/v1/auth/cli/authorize", map[string]string{"state": state})
	assert.Equal(suite.T(), 200, w.Code)

	var response map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(suite.T(), err)

	redirectURL := response["redirect_url"].(string)
	parsed, err := url.Parse(redirectURL)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "127.0.0.1:12345", parsed.Host)
	assert.Empty(suite.T(), parsed.Query().Get("token"))
	assert.NotEmpty(suite.T(), parsed.Query().Get("code"))
	assert.Equal(suite.T(), state, parsed.Query().Get("state"))

	redeemBody := map[string]string{
		"state":         state,
		"code":          parsed.Query().Get("code"),
		"code_verifier": verifier,
	}
	redeemReq, _ := http.NewRequest("POST", "/api/v1/auth/cli/token", strings.NewReader(mustJSON(redeemBody)))
	redeemReq.Header.Set("Content-Type", "application/json")
	redeem := httptest.NewRecorder()
	suite.router.ServeHTTP(redeem, redeemReq)
	assert.Equal(suite.T(), http.StatusOK, redeem.Code)
	assert.Contains(suite.T(), redeem.Body.String(), "\"token\"")

	replay := httptest.NewRecorder()
	replayReq, _ := http.NewRequest("POST", "/api/v1/auth/cli/token", strings.NewReader(mustJSON(redeemBody)))
	replayReq.Header.Set("Content-Type", "application/json")
	suite.router.ServeHTTP(replay, replayReq)
	assert.Equal(suite.T(), http.StatusUnauthorized, replay.Code)
}

func (suite *CLIHandlerTestSuite) TestStartCLIAuthorizationRejectsNonLoopbackCallbacks() {
	for _, callback := range []string{
		"https://attacker.example/callback",
		"http://localhost.attacker.example:12345/callback",
		"javascript:alert(1)",
		"http://user@127.0.0.1:12345/callback",
		"http://127.0.0.1:12345/callback#fragment",
		"http://127.0.0.1/callback",
	} {
		suite.T().Run(callback, func(t *testing.T) {
			verifier := strings.Repeat("a", 43)
			digest := sha256.Sum256([]byte(verifier))
			body := map[string]string{
				"callback_url":          callback,
				"device_name":           "Test Device",
				"code_challenge":        base64.RawURLEncoding.EncodeToString(digest[:]),
				"code_challenge_method": "S256",
			}
			req, _ := http.NewRequest("POST", "/api/v1/auth/cli/start", strings.NewReader(mustJSON(body)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			suite.router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func (suite *CLIHandlerTestSuite) TestGetInstallScript() {
	// Test GET /api/v1/cli/install
	req, _ := http.NewRequest("GET", "/api/v1/cli/install", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), 200, w.Code)
	assert.Equal(suite.T(), "text/x-shellscript", w.Header().Get("Content-Type"))

	body := w.Body.String()
	assert.Contains(suite.T(), body, "#!/bin/bash")
	assert.Contains(suite.T(), body, "curl --fail --silent --show-error --location")
	assert.NotContains(suite.T(), body, "TOKEN=")

	maliciousReq, _ := http.NewRequest("GET", "/api/v1/cli/install?token=$(touch%20/tmp/pwned)", nil)
	maliciousReq.Header.Set("X-Forwarded-Host", "$(id).attacker.example")
	malicious := httptest.NewRecorder()
	suite.router.ServeHTTP(malicious, maliciousReq)
	assert.Equal(suite.T(), body, malicious.Body.String())
	assert.NotContains(suite.T(), malicious.Body.String(), "attacker.example")
}

func (suite *CLIHandlerTestSuite) startCLIAuthorization(callback string) (string, string) {
	verifier := strings.Repeat("a", 43)
	digest := sha256.Sum256([]byte(verifier))
	body := map[string]string{
		"callback_url":          callback,
		"device_name":           "Test Device",
		"code_challenge":        base64.RawURLEncoding.EncodeToString(digest[:]),
		"code_challenge_method": "S256",
	}
	req, _ := http.NewRequest("POST", "/api/v1/auth/cli/start", strings.NewReader(mustJSON(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)
	assert.Equal(suite.T(), http.StatusCreated, w.Code)

	var response map[string]interface{}
	assert.NoError(suite.T(), json.Unmarshal(w.Body.Bytes(), &response))
	return response["state"].(string), verifier
}

func mustJSON(value interface{}) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (suite *CLIHandlerTestSuite) TestDownloadCLIBinary() {
	// Create dummy binary file
	dummyDir := "bin/cli"
	os.MkdirAll(dummyDir, 0755)
	dummyFile := filepath.Join(dummyDir, "scriberr-linux-amd64")
	os.WriteFile(dummyFile, []byte("dummy binary content"), 0755)
	defer os.RemoveAll(dummyDir)

	// Test GET /api/v1/cli/download
	req, _ := http.NewRequest("GET", "/api/v1/cli/download?os=linux&arch=amd64", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), 200, w.Code)
	assert.Equal(suite.T(), "dummy binary content", w.Body.String())
	assert.Contains(suite.T(), w.Header().Get("Content-Disposition"), "attachment")
}

func (suite *CLIHandlerTestSuite) TestDownloadCLIBinaryMissingParams() {
	req, _ := http.NewRequest("GET", "/api/v1/cli/download", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), 400, w.Code)
}

func (suite *CLIHandlerTestSuite) TestDownloadCLIBinaryUnsupported() {
	req, _ := http.NewRequest("GET", "/api/v1/cli/download?os=unknown&arch=amd64", nil)
	w := httptest.NewRecorder()
	suite.router.ServeHTTP(w, req)

	assert.Equal(suite.T(), 400, w.Code)
}

func TestCLIHandlerTestSuite(t *testing.T) {
	suite.Run(t, new(CLIHandlerTestSuite))
}
