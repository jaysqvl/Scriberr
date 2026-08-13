package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"scriberr/internal/auth"
	"scriberr/internal/database"
	"scriberr/internal/models"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware handles both API key and JWT authentication
func AuthMiddleware(authService *auth.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Check for API key first
		apiKey := c.GetHeader("X-API-Key")
		if apiKey != "" {
			if validateAPIKey(apiKey) {
				c.Set("auth_type", "api_key")
				c.Set("api_key", apiKey)
				c.Next()
				return
			}
		}

		// Check for JWT token
		var token string
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			// Extract token from "Bearer <token>"
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && parts[0] == "Bearer" {
				token = parts[1]
			}
		}

		// Fallback to cookie if no header
		if token == "" {
			if cookie, err := c.Cookie("scriberr_access_token"); err == nil {
				token = cookie
			}
		}

		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Missing authentication"})
			c.Abort()
			return
		}

		claims, err := authService.ValidateToken(token)
		if err != nil || !tokenIsCurrent(token, claims) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			c.Abort()
			return
		}

		c.Set("auth_type", "jwt")
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Next()
	}
}

// validateAPIKey validates an API key against the database and updates last used timestamp
func validateAPIKey(key string) bool {
	var apiKey models.APIKey
	result := database.DB.Where("key = ? AND is_active = ?", key, true).First(&apiKey)
	if result.Error != nil {
		return false
	}

	// Update last used timestamp
	now := time.Now()
	apiKey.LastUsed = &now
	database.DB.Save(&apiKey)

	return true
}

// APIKeyOnlyMiddleware only allows API key authentication
func APIKeyOnlyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetHeader("X-API-Key")
		if apiKey == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "API key required"})
			c.Abort()
			return
		}

		if !validateAPIKey(apiKey) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
			c.Abort()
			return
		}

		c.Set("auth_type", "api_key")
		c.Set("api_key", apiKey)
		c.Next()
	}
}

// JWTOnlyMiddleware only allows JWT authentication
func JWTOnlyMiddleware(authService *auth.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header format"})
			c.Abort()
			return
		}

		token := parts[1]
		claims, err := authService.ValidateToken(token)
		if err != nil || !tokenIsCurrent(token, claims) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			c.Abort()
			return
		}

		c.Set("auth_type", "jwt")
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Next()
	}
}

func tokenIsCurrent(rawToken string, claims *auth.Claims) bool {
	if claims == nil || database.DB == nil {
		return false
	}

	var user models.User
	if err := database.DB.Select("id", "token_version").Where("id = ?", claims.UserID).First(&user).Error; err != nil {
		return false
	}
	if user.TokenVersion != claims.TokenVersion {
		return false
	}

	digest := sha256.Sum256([]byte(rawToken))
	var count int64
	if err := database.DB.Model(&models.RevokedAccessToken{}).
		Where("token_hash = ? AND expires_at > ?", hex.EncodeToString(digest[:]), time.Now()).
		Count(&count).Error; err != nil {
		return false
	}
	return count == 0
}
