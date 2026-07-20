package user

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"

	"easygo-agent/internal/model"

	"gorm.io/gorm"
)

// UserService 定义用户业务逻辑接口
type UserService interface {
	CreateUser(ctx context.Context, name, password string, email, phone *string) (*model.User, error)
	FindUserByID(ctx context.Context, id uint64) (*model.User, error)
	FindUserByName(ctx context.Context, name string) (*model.User, error)
	UpdateContact(ctx context.Context, id uint64, email, phone *string) error
	UpdatePassword(ctx context.Context, id uint64, newPassword string) error
	SoftDeleteUser(ctx context.Context, id uint64) error
	VerifyPassword(plainPassword, salt, passwordHash string) bool
}

type userServiceImpl struct {
	db *gorm.DB
}

func NewUserService(db *gorm.DB) UserService {
	return &userServiceImpl{db: db}
}

// CreateUser 创建用户
func (s *userServiceImpl) CreateUser(ctx context.Context, name, password string, email, phone *string) (*model.User, error) {
	salt := generateSalt()
	user := &model.User{
		Name:         name,
		PasswordHash: hashPassword(password, salt),
		Salt:         salt,
		Email:        email,
		Phone:        phone,
	}
	if err := model.CreateUser(ctx, s.db, user); err != nil {
		return nil, err
	}
	return user, nil
}

// FindUserByID 根据 ID 查询
func (s *userServiceImpl) FindUserByID(ctx context.Context, id uint64) (*model.User, error) {
	return model.FindUserByID(ctx, s.db, id)
}

// FindUserByName 根据名称查询
func (s *userServiceImpl) FindUserByName(ctx context.Context, name string) (*model.User, error) {
	return model.FindUserByName(ctx, s.db, name)
}

// UpdateContact 更新联系方式
func (s *userServiceImpl) UpdateContact(ctx context.Context, id uint64, email, phone *string) error {
	return model.UpdateUserContact(ctx, s.db, id, email, phone)
}

// UpdatePassword 更新密码
func (s *userServiceImpl) UpdatePassword(ctx context.Context, id uint64, newPassword string) error {
	salt := generateSalt()
	return model.UpdateUserPassword(ctx, s.db, id, hashPassword(newPassword, salt), salt)
}

// SoftDeleteUser 软删除用户
func (s *userServiceImpl) SoftDeleteUser(ctx context.Context, id uint64) error {
	return model.SoftDeleteUser(ctx, s.db, id)
}

// VerifyPassword 验证密码（登录时使用）
func (s *userServiceImpl) VerifyPassword(plainPassword, salt, passwordHash string) bool {
	return hashPassword(plainPassword, salt) == passwordHash
}

// ========== 内部辅助 ==========

// generateSalt 生成 16 字节随机 salt。
// Go 1.20+ crypto/rand.Read 永不返回 error，无需 panic。
func generateSalt() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// hashPassword MD5(salt + password) → 32 位 hex
func hashPassword(password, salt string) string {
	h := md5.Sum([]byte(salt + password))
	return hex.EncodeToString(h[:])
}
