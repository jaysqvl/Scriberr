package api

import (
	"strings"
	"testing"

	"scriberr/internal/config"
	"scriberr/internal/models"
	"scriberr/internal/transcription"

	"github.com/stretchr/testify/require"
)

func TestIsAllowedYouTubeURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		allowed bool
	}{
		{name: "youtube apex", rawURL: "https://youtube.com/watch?v=abc123", allowed: true},
		{name: "youtube subdomain", rawURL: "https://www.youtube.com/watch?v=abc123", allowed: true},
		{name: "mobile youtube", rawURL: "https://m.youtube.com/watch?v=abc123", allowed: true},
		{name: "short link", rawURL: "https://youtu.be/abc123", allowed: true},
		{name: "http rejected", rawURL: "http://youtube.com/watch?v=abc123", allowed: false},
		{name: "substring host rejected", rawURL: "https://youtube.com.evil.example/watch?v=abc123", allowed: false},
		{name: "query substring rejected", rawURL: "https://evil.example/watch?next=youtube.com", allowed: false},
		{name: "userinfo smuggling rejected", rawURL: "https://youtube.com@127.0.0.1/watch?v=abc123", allowed: false},
		{name: "metadata ip rejected", rawURL: "https://169.254.169.254/latest/meta-data?youtube.com", allowed: false},
		{name: "protocol relative rejected", rawURL: "//youtube.com/watch?v=abc123", allowed: false},
		{name: "javascript rejected", rawURL: "javascript:alert('youtube.com')", allowed: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAllowedYouTubeURL(tt.rawURL); got != tt.allowed {
				t.Fatalf("isAllowedYouTubeURL(%q) = %v, want %v", tt.rawURL, got, tt.allowed)
			}
		})
	}
}

func TestNewHandlerSharesMediaCapacityWithQuickJobs(t *testing.T) {
	cfg := &config.Config{
		UploadDir:             t.TempDir(),
		MaxConcurrentMedia:    1,
		MaxUploadBytes:        1024,
		MediaTimeoutMinutes:   5,
		AuthRateLimitEnabled:  true,
		AuthMaxFailedAttempts: 5,
	}
	quick, err := transcription.NewQuickTranscriptionService(cfg, nil, nil)
	require.NoError(t, err)
	t.Cleanup(quick.Close)

	handler := NewHandler(
		cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, quick, nil, nil,
	)
	release, err := handler.resourceAdmission.tryAcquire()
	require.NoError(t, err)
	defer release()

	_, err = quick.SubmitQuickJob(strings.NewReader("audio"), "audio.wav", models.WhisperXParams{})
	require.ErrorIs(t, err, transcription.ErrQuickCapacity)
}
