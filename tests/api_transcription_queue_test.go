package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"scriberr/internal/models"
)

func (suite *APIHandlerTestSuite) TestQueueRunResolvesProfileSecretsServerSideAndRedactsResponse() {
	job := suite.helper.CreateTestTranscriptionJob(suite.T(), "Profile queue")
	requireStatusUpdate(suite, job.ID, models.StatusCompleted)
	apiKey := "queue-profile-super-secret"
	hfToken := "queue-profile-hf-secret"
	profile := &models.TranscriptionProfile{
		Name: "Authoritative profile",
		Parameters: models.WhisperXParams{
			ModelFamily: "openai",
			Model:       "whisper-1",
			Device:      "cpu",
			APIKey:      &apiKey,
			HfToken:     &hfToken,
		},
	}
	suite.Require().NoError(suite.helper.DB.Create(profile).Error)

	w := suite.makeAuthenticatedRequest(http.MethodPost, "/api/v1/transcription/"+job.ID+"/queue", map[string]any{
		"profile_id":   profile.ID,
		"profile_name": "Untrusted browser label",
		"parameters":   map[string]any{"model": "client-overwrite"},
	}, true)
	suite.Equal(http.StatusCreated, w.Code, w.Body.String())
	suite.NotContains(w.Body.String(), apiKey)
	suite.NotContains(w.Body.String(), hfToken)

	var response struct {
		JobID      string                          `json:"job_id"`
		ActiveItem *models.TranscriptionQueueItem  `json:"active_item"`
		Items      []models.TranscriptionQueueItem `json:"items"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))
	suite.Equal(job.ID, response.JobID)
	suite.Require().NotNil(response.ActiveItem)
	suite.Equal(models.QueueStatusPending, response.ActiveItem.Status)
	suite.Equal("Authoritative profile", *response.ActiveItem.ProfileName)
	suite.Equal("whisper-1", response.ActiveItem.Parameters.Model)

	var raw map[string]any
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &raw))
	active := raw["active_item"].(map[string]any)
	params := active["parameters"].(map[string]any)
	suite.Equal(true, params["has_api_key"])
	suite.Equal(true, params["has_hf_token"])
	_, exposesAPIKey := params["api_key"]
	_, exposesHFToken := params["hf_token"]
	suite.False(exposesAPIKey)
	suite.False(exposesHFToken)

	var stored models.TranscriptionQueueItem
	suite.Require().NoError(suite.helper.DB.First(&stored, "id = ?", response.ActiveItem.ID).Error)
	suite.Require().NotNil(stored.Parameters.APIKey)
	suite.Require().NotNil(stored.Parameters.HfToken)
	suite.Equal(apiKey, *stored.Parameters.APIKey)
	suite.Equal(hfToken, *stored.Parameters.HfToken)

	w = suite.makeAuthenticatedRequest(http.MethodPost, "/api/v1/transcription/"+job.ID+"/kill", map[string]any{
		"queue_item_id": "a-different-promoted-run",
	}, true)
	suite.Equal(http.StatusConflict, w.Code, w.Body.String())
	w = suite.makeAuthenticatedRequest(http.MethodPost, "/api/v1/transcription/"+job.ID+"/kill", map[string]any{}, true)
	suite.Equal(http.StatusConflict, w.Code, w.Body.String())
	var stillPending models.TranscriptionQueueItem
	suite.Require().NoError(suite.helper.DB.First(&stillPending, "id = ?", response.ActiveItem.ID).Error)
	suite.Equal(models.QueueStatusPending, stillPending.Status)
}

func (suite *APIHandlerTestSuite) TestQueueEndpointsReorderCancelAndClearWaitingRuns() {
	job := suite.helper.CreateTestTranscriptionJob(suite.T(), "Manage queue")
	requireStatusUpdate(suite, job.ID, models.StatusProcessing)

	ids := make([]string, 0, 3)
	for _, model := range []string{"tiny", "small", "medium"} {
		w := suite.makeAuthenticatedRequest(http.MethodPost, "/api/v1/transcription/"+job.ID+"/queue", map[string]any{
			"parameters": map[string]any{"model_family": "whisper", "model": model},
		}, true)
		suite.Equal(http.StatusCreated, w.Code, w.Body.String())
		var response struct {
			Items []models.TranscriptionQueueItem `json:"items"`
		}
		suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &response))
		ids = ids[:0]
		for _, item := range response.Items {
			ids = append(ids, item.ID)
		}
	}
	suite.Require().Len(ids, 3)

	reversed := []string{ids[2], ids[1], ids[0]}
	w := suite.makeAuthenticatedRequest(http.MethodPut, "/api/v1/transcription/"+job.ID+"/queue/order", map[string]any{
		"ordered_ids": reversed,
	}, true)
	suite.Equal(http.StatusOK, w.Code, w.Body.String())
	var reordered struct {
		Items []models.TranscriptionQueueItem `json:"items"`
	}
	suite.Require().NoError(json.Unmarshal(w.Body.Bytes(), &reordered))
	suite.Require().Len(reordered.Items, 3)
	suite.Equal([]string{"medium", "small", "tiny"}, []string{
		reordered.Items[0].Parameters.Model,
		reordered.Items[1].Parameters.Model,
		reordered.Items[2].Parameters.Model,
	})

	w = suite.makeAuthenticatedRequest(http.MethodDelete, "/api/v1/transcription/"+job.ID+"/queue/"+reordered.Items[1].ID, nil, true)
	suite.Equal(http.StatusOK, w.Code, w.Body.String())
	w = suite.makeAuthenticatedRequest(http.MethodDelete, "/api/v1/transcription/"+job.ID+"/queue", nil, true)
	suite.Equal(http.StatusOK, w.Code, w.Body.String())
	suite.Contains(w.Body.String(), `"cleared":2`)

	w = suite.makeAuthenticatedRequest(http.MethodGet, "/api/v1/transcription/"+job.ID+"/queue", nil, true)
	suite.Equal(http.StatusOK, w.Code, w.Body.String())
	suite.Contains(w.Body.String(), `"items":[]`)
	suite.Contains(w.Body.String(), `"active_item":null`)
}

func (suite *APIHandlerTestSuite) TestQueueEndpointsRequireAuthentication() {
	job := suite.helper.CreateTestTranscriptionJob(suite.T(), "Protected queue")
	paths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/transcription/" + job.ID + "/queue"},
		{http.MethodPost, "/api/v1/transcription/" + job.ID + "/queue"},
		{http.MethodPut, "/api/v1/transcription/" + job.ID + "/queue/order"},
		{http.MethodDelete, "/api/v1/transcription/" + job.ID + "/queue/item"},
		{http.MethodDelete, "/api/v1/transcription/" + job.ID + "/queue"},
	}
	for _, endpoint := range paths {
		req, err := http.NewRequest(endpoint.method, endpoint.path, strings.NewReader(`{}`))
		suite.Require().NoError(err)
		w := httptest.NewRecorder()
		suite.router.ServeHTTP(w, req)
		suite.Equal(http.StatusUnauthorized, w.Code, endpoint.method+" "+endpoint.path)
	}
}

func (suite *APIHandlerTestSuite) TestKillEmptyObjectTargetsLegacyRunAndBodylessCallsRemainCompatible() {
	legacyTargeted := suite.helper.CreateTestTranscriptionJob(suite.T(), "Targeted legacy stop")
	w := suite.makeAuthenticatedRequest(http.MethodPost, "/api/v1/transcription/"+legacyTargeted.ID+"/kill", map[string]any{}, true)
	suite.Equal(http.StatusOK, w.Code, w.Body.String())

	legacyUnrestricted := suite.helper.CreateTestTranscriptionJob(suite.T(), "Unrestricted legacy stop")
	w = suite.makeAuthenticatedRequest(http.MethodPost, "/api/v1/transcription/"+legacyUnrestricted.ID+"/kill", nil, true)
	suite.Equal(http.StatusOK, w.Code, w.Body.String())
}

func requireStatusUpdate(suite *APIHandlerTestSuite, jobID string, status models.JobStatus) {
	suite.Require().NoError(suite.helper.DB.Model(&models.TranscriptionJob{}).
		Where("id = ?", jobID).Update("status", status).Error)
}
