package models

import (
	"time"
)

// RefreshToken represents a persistent refresh token for rotating access
type RefreshToken struct {
	ID             uint       `json:"id" gorm:"primaryKey"`
	UserID         uint       `json:"user_id" gorm:"not null;index"`
	FamilyID       string     `json:"-" gorm:"not null;index;type:varchar(36)"`
	Hashed         string     `json:"-" gorm:"not null;uniqueIndex;type:varchar(128)"`
	ReplacedByHash *string    `json:"-" gorm:"type:varchar(128)"`
	ExpiresAt      time.Time  `json:"expires_at" gorm:"not null;index"`
	Revoked        bool       `json:"revoked" gorm:"not null;default:false;index"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt      time.Time  `json:"updated_at" gorm:"autoUpdateTime"`
}

// RevokedAccessToken stores only a one-way token digest until its natural
// expiry, allowing logout to invalidate the presented JWT immediately.
type RevokedAccessToken struct {
	TokenHash string    `json:"-" gorm:"primaryKey;type:varchar(64)"`
	ExpiresAt time.Time `json:"expires_at" gorm:"not null;index"`
	CreatedAt time.Time `json:"created_at" gorm:"autoCreateTime"`
}
