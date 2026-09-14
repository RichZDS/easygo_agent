package conversation

import (
	"context"
	"sort"
	"time"
)

func (m *Memory) Transcript(ctx context.Context, user string, after, through time.Time) ([]TranscriptTurn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateUser(user); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []TranscriptTurn{}
	for id, entry := range m.entries {
		if entry.session.Username != user {
			continue
		}
		for _, turn := range entry.turns {
			if turn.Status != "completed" || !turn.CreatedAt.After(after) || turn.CreatedAt.After(through) {
				continue
			}
			copy := turn
			var err error
			copy.Messages, err = Clone(turn.Messages)
			if err != nil {
				return nil, err
			}
			result = append(result, TranscriptTurn{SessionID: id, Turn: copy})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func (m *Memory) UsersWithTranscript(ctx context.Context, after, through time.Time) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	users := map[string]struct{}{}
	for _, entry := range m.entries {
		for _, turn := range entry.turns {
			if turn.Status == "completed" && turn.CreatedAt.After(after) && !turn.CreatedAt.After(through) {
				users[entry.session.Username] = struct{}{}
				break
			}
		}
	}
	result := make([]string, 0, len(users))
	for user := range users {
		result = append(result, user)
	}
	sort.Strings(result)
	return result, nil
}
