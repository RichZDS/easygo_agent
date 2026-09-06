// Package conversation stores Eino messages without a parallel chat schema.
package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

var (
	ErrNotFound    = errors.New("conversation not found")
	ErrBusy        = errors.New("conversation already running")
	ErrInvalidUser = errors.New("username must contain 1 to 128 characters")
)

type Session struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Turn is an audit record. Messages are the complete native outputs received,
// including on failed/canceled runs. Context is committed separately.
type Turn struct {
	ID        int64                    `json:"id"`
	Status    string                   `json:"status"`
	Messages  []*schema.AgenticMessage `json:"messages"`
	CreatedAt time.Time                `json:"created_at"`
}

type Store interface {
	Create(context.Context, string) (Session, error)
	List(context.Context, string, int, int) ([]Session, error)
	History(context.Context, string, string, int64, int) ([]Turn, error)
	Begin(context.Context, string, string) (Lease, error)
	Close()
}

// Lease exclusively owns one conversation until Close. Commit atomically saves
// the next model context and one audit turn; callers must always close the lease.
type Lease interface {
	Messages() []*schema.AgenticMessage
	Commit(context.Context, []*schema.AgenticMessage, Turn) error
	Close()
}

func ValidateUser(user string) error {
	if strings.TrimSpace(user) != user || user == "" || len([]rune(user)) > 128 {
		return ErrInvalidUser
	}
	return nil
}

func Clone(messages []*schema.AgenticMessage) ([]*schema.AgenticMessage, error) {
	data, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	var result []*schema.AgenticMessage
	err = json.Unmarshal(data, &result)
	return result, err
}

func pageSize(limit int) int {
	if limit < 1 || limit > 100 {
		return 50
	}
	return limit
}
