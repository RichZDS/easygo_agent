package account

import (
	"context"
	"easygo-agent/internal/auth"
	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/service/user"
	"fmt"
)

type Service struct {
	users  user.UserService
	issuer *auth.Issuer
}

func New(users user.UserService, issuer *auth.Issuer) *Service {
	return &Service{users: users, issuer: issuer}
}
func (s *Service) Login(ctx context.Context, name, password string) (string, error) {
	u, err := s.users.FindUserByName(ctx, name)
	if err != nil {
		return "", errorcode.New(errorcode.Unauthorized, "invalid credentials")
	}
	if !s.users.VerifyPassword(password, u.Salt, u.PasswordHash) {
		return "", errorcode.New(errorcode.Unauthorized, "invalid credentials")
	}
	token, err := s.issuer.Issue(u.ID)
	if err != nil {
		return "", fmt.Errorf("issue access token: %w", err)
	}
	return token, nil
}
