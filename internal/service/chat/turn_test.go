package chat

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/model"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type recordedEvent struct {
	name string
	data any
}

type recordingEventSink struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (s *recordingEventSink) Emit(_ context.Context, _ string, event string, data any) error {
	s.mu.Lock()
	s.events = append(s.events, recordedEvent{name: event, data: data})
	s.mu.Unlock()
	return nil
}

func newMockGORM(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	return db, mock
}

func turnRows(status uint8) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows([]string{
		"id", "turn_id", "user_id", "chat_session_id", "model_config_id",
		"model_revision", "request_id", "input", "status", "created_at", "updated_at",
	}).AddRow(1, "turn-1", 7, 11, 13, 1, "request-1", "hello", status, now, now)
}

func TestCancelRunningTurnIsIdempotentAndInterruptsProvider(t *testing.T) {
	db, mock := newMockGORM(t)
	events := &recordingEventSink{}
	coordinator := NewCancellationCoordinator()
	var cancelled atomic.Int32
	unregister := coordinator.Register("turn-1", func() { cancelled.Add(1) })
	defer unregister()
	service := NewTurnService(db, events, coordinator)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .*FROM `chat_turn`.*turn_id = .*user_id = .*FOR UPDATE").
		WithArgs("turn-1", uint64(7), 1).
		WillReturnRows(turnRows(model.TurnRunning))
	mock.ExpectExec("UPDATE `chat_turn` SET .*status.*WHERE id = .*status =").
		WithArgs(sqlmock.AnyArg(), model.TurnCancelled, sqlmock.AnyArg(), uint64(1), model.TurnRunning).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	turn, err := service.Cancel(context.Background(), 7, "turn-1")
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if turn.Status != model.TurnCancelled {
		t.Fatalf("turn status = %d", turn.Status)
	}
	if cancelled.Load() != 1 {
		t.Fatalf("provider cancel calls = %d", cancelled.Load())
	}
	if len(events.events) != 0 {
		t.Fatalf("running cancel emitted controller events = %#v", events.events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelAlreadyCancelledTurnDoesNotEmitAgain(t *testing.T) {
	db, mock := newMockGORM(t)
	events := &recordingEventSink{}
	service := NewTurnService(db, events, NewCancellationCoordinator())

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .*FROM `chat_turn`.*turn_id = .*user_id = .*FOR UPDATE").
		WithArgs("turn-1", uint64(7), 1).
		WillReturnRows(turnRows(model.TurnCancelled))
	mock.ExpectCommit()

	turn, err := service.Cancel(context.Background(), 7, "turn-1")
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if turn.Status != model.TurnCancelled {
		t.Fatalf("turn status = %d", turn.Status)
	}
	if len(events.events) != 0 {
		t.Fatalf("events = %#v", events.events)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelRejectsTurnOwnedByAnotherUser(t *testing.T) {
	db, mock := newMockGORM(t)
	service := NewTurnService(db, &recordingEventSink{}, NewCancellationCoordinator())

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .*FROM `chat_turn`.*turn_id = .*user_id = .*FOR UPDATE").
		WithArgs("turn-1", uint64(99), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	_, err := service.Cancel(context.Background(), 99, "turn-1")
	if errorcode.From(err).Code != errorcode.NotFound {
		t.Fatalf("Cancel() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
