package service

import (
	"context"

	"easygo-agent/internal/model"
	"easygo-agent/internal/repository"
)

type CreateUserInput struct {
	Name  string
	Email string
}

type UserService interface {
	Create(ctx context.Context, input CreateUserInput) (*model.User, error)
	Get(ctx context.Context, id uint64) (*model.User, error)
}

type userService struct {
	users repository.UserRepository
}

func NewUserService(users repository.UserRepository) UserService {
	return &userService{users: users}
}

func (s *userService) Create(ctx context.Context, input CreateUserInput) (*model.User, error) {
	user := &model.User{Name: input.Name, Email: input.Email}
	if err := s.users.Create(ctx, user); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *userService) Get(ctx context.Context, id uint64) (*model.User, error) {
	return s.users.FindByID(ctx, id)
}
