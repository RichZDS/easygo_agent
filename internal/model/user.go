package model

import (
	"time"

	"gorm.io/gorm"
)

// User maps to the MySQL `user` table. PasswordHash and Salt are persistence
// fields only and must never be serialized into an HTTP response.
type User struct {
	ID           uint64         `gorm:"column:id;type:bigint unsigned;primaryKey;autoIncrement" json:"id"`
	Name         string         `gorm:"column:name;type:varchar(64);not null;uniqueIndex:uk_user_name" json:"name"`
	PasswordHash string         `gorm:"column:password;type:varchar(255);not null" json:"-"`
	Salt         string         `gorm:"column:salt;type:varchar(64);not null" json:"-"`
	Email        *string        `gorm:"column:email;type:varchar(128);uniqueIndex:uk_user_email" json:"email"`
	Phone        *string        `gorm:"column:phone;type:varchar(32);uniqueIndex:uk_user_phone" json:"phone"`
	CreatedAt    time.Time      `gorm:"column:created_at;not null;autoCreateTime;index:idx_user_deleted_created,priority:2" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"column:updated_at;not null;autoUpdateTime" json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;index:idx_user_deleted_created,priority:1" json:"-"`
}

func (User) TableName() string { return "user" }
