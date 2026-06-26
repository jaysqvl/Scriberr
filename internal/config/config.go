package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"scriberr/pkg/logger"

	"github.com/joho/godotenv"
)

// Config holds all configuration values
type Config struct {
	// Server configuration
	Port string
	Host string

	// Database configuration
	DatabasePath string

	// JWT configuration
	JWTSecret string

	// File storage
	UploadDir             string
	TranscriptsDir        string
	TempDir               string
	UploadChunkSizeBytes  int64
	UploadSessionTTLHours int

	// Python/WhisperX configuration
	WhisperXEnv string

	// Environment configuration
	Environment    string
	AllowedOrigins []string
	SecureCookies  bool // Explicit control over Secure flag (for HTTPS deployments)
	TrustedProxies []string

	// Authentication abuse protection
	AuthRateLimitEnabled     bool
	AuthMaxFailedAttempts    int
	AuthFailureWindowSeconds int
	AuthLockoutSeconds       int
	AuthIPMaxFailedAttempts  int

	// OpenAI configuration
	OpenAIAPIKey string

	// Hugging Face configuration
	HFToken string
}

// Load loads configuration from environment variables and .env file
func Load() *Config {
	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		logger.Debug("No .env file found, using system environment variables")
	}

	// Default SecureCookies to true in production, false otherwise
	defaultSecure := "false"
	if strings.ToLower(getEnv("APP_ENV", "development")) == "production" {
		defaultSecure = "true"
	}

	chunkSizeMB := clampInt(getEnvInt("UPLOAD_CHUNK_SIZE_MB", 50), 1, 90)
	sessionTTLHours := clampInt(getEnvInt("UPLOAD_SESSION_TTL_HOURS", 48), 1, 24*14)

	return &Config{
		Port:                     getEnv("PORT", "8080"),
		Host:                     getEnv("HOST", "0.0.0.0"),
		Environment:              getEnv("APP_ENV", "development"),
		AllowedOrigins:           strings.Split(getEnv("ALLOWED_ORIGINS", "http://localhost:5173,http://localhost:8080"), ","),
		DatabasePath:             getEnv("DATABASE_PATH", "data/scriberr.db"),
		JWTSecret:                getJWTSecret(),
		UploadDir:                getEnv("UPLOAD_DIR", "data/uploads"),
		TranscriptsDir:           getEnv("TRANSCRIPTS_DIR", "data/transcripts"),
		TempDir:                  getEnv("TEMP_DIR", "data/temp"),
		UploadChunkSizeBytes:     int64(chunkSizeMB) * 1024 * 1024,
		UploadSessionTTLHours:    sessionTTLHours,
		WhisperXEnv:              getEnv("WHISPERX_ENV", "data/whisperx-env"),
		SecureCookies:            getEnv("SECURE_COOKIES", defaultSecure) == "true",
		TrustedProxies:           splitCSV(getEnv("TRUSTED_PROXIES", "")),
		AuthRateLimitEnabled:     getEnvBool("AUTH_RATE_LIMIT_ENABLED", true),
		AuthMaxFailedAttempts:    getEnvInt("AUTH_MAX_FAILED_ATTEMPTS", 5),
		AuthFailureWindowSeconds: getEnvInt("AUTH_FAILURE_WINDOW_SECONDS", 600),
		AuthLockoutSeconds:       getEnvInt("AUTH_LOCKOUT_SECONDS", 900),
		AuthIPMaxFailedAttempts:  getEnvInt("AUTH_IP_MAX_FAILED_ATTEMPTS", 20),
		OpenAIAPIKey:             getEnv("OPENAI_API_KEY", ""),
		HFToken:                  getEnv("HF_TOKEN", ""),
	}
}

// IsProduction returns true if the environment is production
func (c *Config) IsProduction() bool {
	return strings.ToLower(c.Environment) == "production"
}

// getEnv gets an environment variable with a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.Atoi(value)
		if err == nil {
			return parsed
		}
		logger.Warn("Invalid integer environment variable, using default", "key", key, "value", value, "default", defaultValue)
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err == nil {
			return parsed
		}
		logger.Warn("Invalid boolean environment variable, using default", "key", key, "value", value, "default", defaultValue)
	}
	return defaultValue
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

// getJWTSecret gets JWT secret from env or generates a secure random one
func getJWTSecret() string {
	if secret := os.Getenv("JWT_SECRET"); secret != "" {
		return secret
	}
	// Persist a dev secret across restarts to avoid invalidating tokens
	secretFile := getEnv("JWT_SECRET_FILE", "data/jwt_secret")
	if data, err := os.ReadFile(secretFile); err == nil && len(data) > 0 {
		return strings.TrimSpace(string(data))
	}
	// Generate a secure random JWT secret and persist it
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		logger.Warn("Could not generate secure JWT secret, using fallback", "error", err)
		return "fallback-jwt-secret-please-set-JWT_SECRET-env-var"
	}
	secret := hex.EncodeToString(bytes)
	// Ensure dir exists and write file (best-effort)
	_ = os.MkdirAll(filepath.Dir(secretFile), 0755)
	_ = os.WriteFile(secretFile, []byte(secret), 0600)
	logger.Debug("Generated persistent JWT secret", "path", secretFile)
	return secret
}
