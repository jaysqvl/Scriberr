package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	cliAuthorizationTTL         = 5 * time.Minute
	maxPendingCLIAuthorizations = 1024
)

type startCLIAuthorizationRequest struct {
	CallbackURL   string `json:"callback_url" binding:"required"`
	DeviceName    string `json:"device_name" binding:"required,max=100"`
	CodeChallenge string `json:"code_challenge" binding:"required"`
	CodeMethod    string `json:"code_challenge_method" binding:"required"`
}

type confirmCLIAuthorizationRequest struct {
	State string `json:"state" binding:"required"`
}

type redeemCLIAuthorizationRequest struct {
	State        string `json:"state" binding:"required"`
	Code         string `json:"code" binding:"required"`
	CodeVerifier string `json:"code_verifier" binding:"required"`
}

type cliAuthorization struct {
	State         string
	CallbackURL   string
	DeviceName    string
	CodeChallenge string
	CodeHash      [sha256.Size]byte
	UserID        uint
	Username      string
	Approved      bool
	Used          bool
	ExpiresAt     time.Time
}

type cliAuthorizationStore struct {
	mu       sync.Mutex
	requests map[string]*cliAuthorization
}

func newCLIAuthorizationStore() *cliAuthorizationStore {
	return &cliAuthorizationStore{requests: make(map[string]*cliAuthorization)}
}

// StartCLIAuthorization creates a short-lived, state-bound PKCE transaction.
// The CLI calls this endpoint before opening the browser.
func (h *Handler) StartCLIAuthorization(c *gin.Context) {
	var req startCLIAuthorizationRequest
	if err := bindLimitedJSON(c, &req, maxAuthBodyBytes); err != nil {
		c.JSON(requestBodyErrorStatus(err), gin.H{"error": "Invalid CLI authorization request"})
		return
	}

	callbackURL, err := validateLoopbackCallback(req.CallbackURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.CodeMethod != "S256" || !validPKCEValue(req.CodeChallenge, 43, 43) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "PKCE S256 code challenge is required"})
		return
	}
	deviceName := strings.TrimSpace(req.DeviceName)
	if deviceName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Device name is required"})
		return
	}

	state, err := randomURLToken(32)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start CLI authorization"})
		return
	}

	h.cliAuthStore.mu.Lock()
	h.cliAuthStore.cleanupExpiredLocked(time.Now())
	if len(h.cliAuthStore.requests) >= maxPendingCLIAuthorizations {
		h.cliAuthStore.mu.Unlock()
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many pending CLI authorization requests"})
		return
	}
	h.cliAuthStore.requests[state] = &cliAuthorization{
		State:         state,
		CallbackURL:   callbackURL.String(),
		DeviceName:    deviceName,
		CodeChallenge: req.CodeChallenge,
		ExpiresAt:     time.Now().Add(cliAuthorizationTTL),
	}
	h.cliAuthStore.mu.Unlock()

	c.JSON(http.StatusCreated, gin.H{
		"state":      state,
		"expires_in": int(cliAuthorizationTTL.Seconds()),
	})
}

// AuthorizeCLI returns the server-owned transaction details for confirmation.
func (h *Handler) AuthorizeCLI(c *gin.Context) {
	state := c.Query("state")
	authorization, ok := h.cliAuthStore.getPending(state)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CLI authorization request is invalid or expired"})
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	u, err := h.userRepo.FindByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user"})
		return
	}

	callbackURL, _ := url.Parse(authorization.CallbackURL)
	c.JSON(http.StatusOK, gin.H{
		"status": "pending",
		"user": gin.H{
			"id":       u.ID,
			"username": u.Username,
		},
		"device_name": authorization.DeviceName,
		"destination": callbackURL.Host,
	})
}

// ConfirmCLIAuthorization approves a pending transaction and returns a
// redirect containing only a short-lived, single-use code.
func (h *Handler) ConfirmCLIAuthorization(c *gin.Context) {
	var req confirmCLIAuthorizationRequest
	if err := bindLimitedJSON(c, &req, maxAuthBodyBytes); err != nil {
		c.JSON(requestBodyErrorStatus(err), gin.H{"error": "Invalid CLI authorization request"})
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
		return
	}
	u, err := h.userRepo.FindByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch user"})
		return
	}

	code, err := randomURLToken(32)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to authorize CLI"})
		return
	}
	codeHash := sha256.Sum256([]byte(code))

	h.cliAuthStore.mu.Lock()
	h.cliAuthStore.cleanupExpiredLocked(time.Now())
	authorization, ok := h.cliAuthStore.requests[req.State]
	if !ok || authorization.Approved || authorization.Used {
		h.cliAuthStore.mu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": "CLI authorization request is invalid or expired"})
		return
	}
	authorization.Approved = true
	authorization.CodeHash = codeHash
	authorization.UserID = u.ID
	authorization.Username = u.Username
	callback := authorization.CallbackURL
	h.cliAuthStore.mu.Unlock()

	callbackURL, _ := url.Parse(callback)
	query := callbackURL.Query()
	query.Set("code", code)
	query.Set("state", req.State)
	callbackURL.RawQuery = query.Encode()

	c.JSON(http.StatusOK, gin.H{"redirect_url": callbackURL.String()})
}

// RedeemCLIAuthorization exchanges the one-time code and PKCE verifier for a
// revocable CLI token. A code is consumed before any token leaves the server.
func (h *Handler) RedeemCLIAuthorization(c *gin.Context) {
	var req redeemCLIAuthorizationRequest
	if err := bindLimitedJSON(c, &req, maxAuthBodyBytes); err != nil {
		c.JSON(requestBodyErrorStatus(err), gin.H{"error": "Invalid CLI token request"})
		return
	}
	if !validPKCEValue(req.CodeVerifier, 43, 128) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid PKCE verifier"})
		return
	}

	codeHash := sha256.Sum256([]byte(req.Code))
	challengeHash := sha256.Sum256([]byte(req.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])

	h.cliAuthStore.mu.Lock()
	h.cliAuthStore.cleanupExpiredLocked(time.Now())
	authorization, ok := h.cliAuthStore.requests[req.State]
	if !ok || !authorization.Approved || authorization.Used ||
		subtle.ConstantTimeCompare(codeHash[:], authorization.CodeHash[:]) != 1 ||
		subtle.ConstantTimeCompare([]byte(challenge), []byte(authorization.CodeChallenge)) != 1 {
		h.cliAuthStore.mu.Unlock()
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired CLI authorization code"})
		return
	}
	authorization.Used = true
	userID := authorization.UserID
	username := authorization.Username
	h.cliAuthStore.mu.Unlock()

	u, err := h.userRepo.FindByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "User no longer exists"})
		return
	}
	token, err := h.authService.GenerateLongLivedToken(u)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate CLI token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": token, "username": username})
}

func (s *cliAuthorizationStore) getPending(state string) (cliAuthorization, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(time.Now())
	authorization, ok := s.requests[state]
	if !ok || authorization.Approved || authorization.Used {
		return cliAuthorization{}, false
	}
	return *authorization, true
}

func (s *cliAuthorizationStore) cleanupExpiredLocked(now time.Time) {
	for state, authorization := range s.requests {
		if !authorization.ExpiresAt.After(now) || authorization.Used {
			delete(s.requests, state)
		}
	}
}

func validateLoopbackCallback(rawURL string) (*url.URL, error) {
	callbackURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !callbackURL.IsAbs() || callbackURL.Opaque != "" {
		return nil, fmt.Errorf("Callback URL must be an absolute loopback URL")
	}
	if callbackURL.Scheme != "http" || callbackURL.User != nil || callbackURL.Fragment != "" || callbackURL.RawQuery != "" {
		return nil, fmt.Errorf("Callback URL must use plain HTTP without credentials, query, or fragment")
	}
	host := strings.ToLower(strings.TrimSuffix(callbackURL.Hostname(), "."))
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return nil, fmt.Errorf("Callback URL must target localhost")
	}
	port, err := strconv.Atoi(callbackURL.Port())
	if err != nil || port < 1024 || port > 65535 {
		return nil, fmt.Errorf("Callback URL must include a non-privileged port")
	}
	return callbackURL, nil
}

func randomURLToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validPKCEValue(value string, minLength, maxLength int) bool {
	if len(value) < minLength || len(value) > maxLength {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-._~", char) {
			continue
		}
		return false
	}
	return true
}
