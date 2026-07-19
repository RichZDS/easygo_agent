package repository

import (
	"context"
	"errors"
	"fmt"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/model"

	"gorm.io/gorm"
)

type UserRepository interface {
	Create(ctx context.Context, user *model.User) error
	FindByID(ctx context.Context, id uint64) (*model.User, error)
	FindByName(ctx context.Context, name string) (*model.User, error)
	UpdateContact(ctx context.Context, id uint64, email, phone *string) error
	UpdatePassword(ctx context.Context, id uint64, passwordHash, salt string) error
	SoftDelete(ctx context.Context, id uint64) error
}

type userRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{db: db}
}

func (r *userRepository) Create(ctx context.Context, user *model.User) error {
	if err := r.db.WithContext(ctx).Create(user).Error; err != nil {
		return mapUserWriteError("create user", err)
	}
	return nil
}

func (r *userRepository) FindByID(ctx context.Context, id uint64) (*model.User, error) {
	return r.findOne(r.db.WithContext(ctx).Where("id = ?", id))
}

func (r *userRepository) FindByName(ctx context.Context, name string) (*model.User, error) {
	return r.findOne(r.db.WithContext(ctx).Where("name = ?", name))
}

func (r *userRepository) UpdateContact(ctx context.Context, id uint64, email, phone *string) error {
	result := r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(map[string]any{
		"email": email,
		"phone": phone,
	})
	return checkUserUpdate("update user contact", result)
}

func (r *userRepository) UpdatePassword(ctx context.Context, id uint64, passwordHash, salt string) error {
	result := r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(map[string]any{
		"password": passwordHash,
		"salt":     salt,
	})
	return checkUserUpdate("update user password", result)
}

func (r *userRepository) SoftDelete(ctx context.Context, id uint64) error {
	result := r.db.WithContext(ctx).Delete(&model.User{}, id)
	return checkUserUpdate("delete user", result)
}

func (r *userRepository) findOne(query *gorm.DB) (*model.User, error) {
	var user model.User
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
