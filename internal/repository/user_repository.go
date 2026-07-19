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
}

type userRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{db: db}
}

func (r *userRepository) Create(ctx context.Context, user *model.User) error {
	if err := r.db.WithContext(ctx).Create(user).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return errorcode.New(errorcode.Conflict, "邮箱已被使用")
		}
		return errorcode.Wrap(errorcode.Database, fmt.Errorf("create user: %w", err))
	}
	return nil
}

func (r *userRepository) FindByID(ctx context.Context, id uint64) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errorcode.New(errorcode.NotFound, "用户不存在")
		}
		return nil, errorcode.Wrap(errorcode.Database, fmt.Errorf("find user: %w", err))
	}
	return &user, nil
}
