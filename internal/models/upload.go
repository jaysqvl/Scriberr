package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type UploadSessionKind string

const (
	UploadKindAudio      UploadSessionKind = "audio"
	UploadKindVideo      UploadSessionKind = "video"
	UploadKindQuick      UploadSessionKind = "quick"
	UploadKindMultiTrack UploadSessionKind = "multitrack"
	UploadKindSubmit     UploadSessionKind = "submit"
)

type UploadSessionStatus string

const (
	UploadSessionActive    UploadSessionStatus = "active"
	UploadSessionCompleted UploadSessionStatus = "completed"
	UploadSessionCancelled UploadSessionStatus = "cancelled"
)

type UploadFileRole string

const (
	UploadFileRoleAudio UploadFileRole = "audio"
	UploadFileRoleVideo UploadFileRole = "video"
	UploadFileRoleAup   UploadFileRole = "aup"
	UploadFileRoleTrack UploadFileRole = "track"
)

// UploadSession tracks a resumable upload across chunk requests.
type UploadSession struct {
	ID             string              `json:"id" gorm:"primaryKey;type:varchar(36)"`
	Kind           UploadSessionKind   `json:"kind" gorm:"type:varchar(20);not null;index"`
	Status         UploadSessionStatus `json:"status" gorm:"type:varchar(20);not null;default:'active';index"`
	TokenHash      string              `json:"-" gorm:"type:varchar(64);not null"`
	Title          *string             `json:"title,omitempty" gorm:"type:text"`
	ProfileName    *string             `json:"profile_name,omitempty" gorm:"type:text"`
	ParametersJSON *string             `json:"parameters_json,omitempty" gorm:"type:text"`
	ChunkSize      int64               `json:"chunk_size" gorm:"not null"`
	ResultID       *string             `json:"result_id,omitempty" gorm:"type:varchar(64);index"`
	ResultType     *string             `json:"result_type,omitempty" gorm:"type:varchar(32)"`
	ExpiresAt      time.Time           `json:"expires_at" gorm:"not null;index"`
	CreatedAt      time.Time           `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt      time.Time           `json:"updated_at" gorm:"autoUpdateTime"`

	Files []UploadSessionFile `json:"files,omitempty" gorm:"foreignKey:UploadSessionID;constraint:OnDelete:CASCADE"`
}

func (us *UploadSession) BeforeCreate(tx *gorm.DB) error {
	if us.ID == "" {
		us.ID = uuid.New().String()
	}
	return nil
}

// UploadSessionFile tracks chunk state for one file inside a resumable session.
type UploadSessionFile struct {
	ID              string         `json:"id" gorm:"primaryKey;type:varchar(80)"`
	UploadSessionID string         `json:"upload_session_id" gorm:"primaryKey;type:varchar(36);not null;index"`
	Role            UploadFileRole `json:"role" gorm:"type:varchar(20);not null;index"`
	OriginalName    string         `json:"original_name" gorm:"type:text;not null"`
	ContentType     string         `json:"content_type" gorm:"type:text"`
	Size            int64          `json:"size" gorm:"not null"`
	LastModified    int64          `json:"last_modified"`
	ChunkCount      int            `json:"chunk_count" gorm:"not null"`
	ReceivedChunks  string         `json:"received_chunks" gorm:"type:text;not null;default:'[]'"`
	ReceivedBytes   int64          `json:"received_bytes" gorm:"not null;default:0"`
	AssembledPath   *string        `json:"assembled_path,omitempty" gorm:"type:text"`
	CreatedAt       time.Time      `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt       time.Time      `json:"updated_at" gorm:"autoUpdateTime"`

	UploadSession UploadSession `json:"-" gorm:"foreignKey:UploadSessionID;constraint:OnDelete:CASCADE"`
}
