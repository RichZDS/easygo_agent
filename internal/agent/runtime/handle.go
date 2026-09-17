package agentruntime

import (
	"context"
	"sync"

	"easygo-agent/internal/conversation"
)

type queueRunHandle struct {
	manager *queueManager
	user    string
	session string
	mu      sync.Mutex
	record  conversation.RunRecord
}

func (handle *queueRunHandle) Run() conversation.RunRecord {
	if handle.manager == nil {
		handle.mu.Lock()
		defer handle.mu.Unlock()
		return conversation.CloneRunRecord(handle.record)
	}
	handle.mu.Lock()
	runID := handle.record.ID
	handle.mu.Unlock()
	record, err := handle.manager.Get(context.Background(), handle.user, handle.session, runID)
	if err != nil {
		handle.mu.Lock()
		defer handle.mu.Unlock()
		return conversation.CloneRunRecord(handle.record)
	}
	handle.mu.Lock()
	handle.record = record
	result := conversation.CloneRunRecord(handle.record)
	handle.mu.Unlock()
	return result
}

func (handle *queueRunHandle) ID() string {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.record.ID
}

func (handle *queueRunHandle) Cancel() {
	if handle.manager != nil {
		_, _ = handle.manager.Cancel(context.Background(), handle.user, handle.session, handle.ID())
	}
}

func (handle *queueRunHandle) Close() {}
