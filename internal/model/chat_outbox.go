package model

import "time"

const (
	OutboxPending   uint8 = 1
	OutboxPublished uint8 = 2
)

type ChatOutbox struct {
	ID          uint64  `gorm:"primaryKey"`
	TurnID      string  `gorm:"type:varchar(64);uniqueIndex"`
	UserID      uint64  `gorm:"not null;index"`
	Status      uint8   `gorm:"not null"`
	StreamID    *string `gorm:"type:varchar(64)"`
	Attempts    uint32  `gorm:"not null"`
	CreatedAt   time.Time
	PublishedAt *time.Time
	UpdatedAt   time.Time
}

func (ChatOutbox) TableName() string { return "chat_outbox" }
