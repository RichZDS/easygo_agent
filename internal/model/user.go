package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"easygo-agent/internal/errorcode"

	"gorm.io/gorm"
)

// User maps to the MySQL `user` table. PasswordHash and Salt are persistence
// fields only and must never be serialized into an HTTP response.
type User struct {
	ID           uint64         `gorm:"column:id;type:bigint unsigned;primaryKey;autoIncrement" json:"id"` // 主键 ID
	Name         string         `gorm:"column:name;type:varchar(64);not null;uniqueIndex:uk_user_name" json:"name"`
	PasswordHash string         `gorm:"column:password;type:varchar(255);not null" json:"-"`                                                   // 密码哈希
	Salt         string         `gorm:"column:salt;type:varchar(64);not null" json:"-"`                                                        // 盐
	Email        *string        `gorm:"column:email;type:varchar(128);uniqueIndex:uk_user_email" json:"email"`                                 // 邮箱
	Phone        *string        `gorm:"column:phone;type:varchar(32);uniqueIndex:uk_user_phone" json:"phone"`                                  // 电话
	CreatedAt    time.Time      `gorm:"column:created_at;not null;autoCreateTime;index:idx_user_deleted_created,priority:2" json:"created_at"` // 创建时间
	UpdatedAt    time.Time      `gorm:"column:updated_at;not null;autoUpdateTime" json:"updated_at"`                                           // 更新时间
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;index:idx_user_deleted_created,priority:1" json:"-"`                                  // 删除时间
}

func (User) TableName() string { return "user" }

// ========== GORM 包级查询函数 ==========

// CreateUser 创建用户
func CreateUser(ctx context.Context, db *gorm.DB, user *User) error {
	if err := db.WithContext(ctx).Create(user).Error; err != nil {
		return mapUserWriteError("create user", err)
	}
	return nil
}

// FindUserByID 根据 ID 查询
func FindUserByID(ctx context.Context, db *gorm.DB, id uint64) (*User, error) {
	return findOneUser(db.WithContext(ctx).Where("id = ?", id))
}

// FindUserByName 根据名称查询
func FindUserByName(ctx context.Context, db *gorm.DB, name string) (*User, error) {
	return findOneUser(db.WithContext(ctx).Where("name = ?", name))
}

// UpdateUserContact 更新邮箱和电话
func UpdateUserContact(ctx context.Context, db *gorm.DB, id uint64, email, phone *string) error {
	result := db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(map[string]any{
		"email": email,
		"phone": phone,
	})
	return checkUserUpdate("update user contact", result)
}

// UpdateUserPassword 更新密码
func UpdateUserPassword(ctx context.Context, db *gorm.DB, id uint64, passwordHash, salt string) error {
	result := db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(map[string]any{
		"password": passwordHash,
		"salt":     salt,
	})
	return checkUserUpdate("update user password", result)
}

// SoftDeleteUser 软删除
func SoftDeleteUser(ctx context.Context, db *gorm.DB, id uint64) error {
	result := db.WithContext(ctx).Delete(&User{}, id)
	return checkUserUpdate("delete user", result)
}

// ========== 内部辅助函数 ==========

func findOneUser(query *gorm.DB) (*User, error) {
	var user User
	if err := query.First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorcode.New(errorcode.NotFound, "用户不存在")
		}
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find user: %w", err))
	}
	return &user, nil
}

func checkUserUpdate(operation string, result *gorm.DB) error {
	if result.Error != nil {
		return mapUserWriteError(operation, result.Error)
	}
	if result.RowsAffected == 0 {
		return errorcode.New(errorcode.NotFound, "用户不存在")
	}
	return nil
}

func mapUserWriteError(operation string, err error) error {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return errorcode.New(errorcode.Conflict, "用户名、邮箱或手机号已存在")
	}
	return errorcode.Wrap(errorcode.Database, fmt.Errorf("%s: %w", operation, err))
}
