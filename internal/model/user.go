package model

import (
	"time"

	"gorm.io/gorm"
)

// User is a persistence model. API request/response DTOs live outside model so
// database details are not accidentally exposed to clients.
type User struct {
	ID        uint64         `gorm:"primaryKey" json:"id"`
	Name      string         `gorm:"size:64;not null" json:"name"`
	Email     string         `gorm:"size:191;not null;uniqueIndex" json:"email"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (User) TableName() string { return "users" }
