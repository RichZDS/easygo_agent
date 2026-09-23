package conversation

import (
	"cmp"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

const (
	MaxProfileMemories = 5
	memoryStateActive  = "active"
	memoryStateArchive = "archived"
)

var (
	ErrInvalidMemory          = errors.New("invalid long-term memory")
	ErrTooManyProfileMemories = errors.New("a user profile may contain at most five memories")
)

func validateDraft(d MemoryDraft) error {
	if !d.Kind.Valid() || strings.TrimSpace(d.Content) == "" || len([]rune(d.Content)) > 4000 {
		return ErrInvalidMemory
	}
	if d.Importance < 0 || d.Importance > 1 || d.Confidence < 0 || d.Confidence > 1 {
		return ErrInvalidMemory
	}
	return nil
}

func cloneLongTermMemory(in LongTermMemory) LongTermMemory {
	in.Tags = slices.Clone(in.Tags)
	in.SourceSessions = slices.Clone(in.SourceSessions)
	in.SourceTurnIDs = slices.Clone(in.SourceTurnIDs)
	if in.LastAccessedAt != nil {
		value := *in.LastAccessedAt
		in.LastAccessedAt = &value
	}
	if in.ExpiresAt != nil {
		value := *in.ExpiresAt
		in.ExpiresAt = &value
	}
	if in.ArchivedAt != nil {
		value := *in.ArchivedAt
		in.ArchivedAt = &value
	}
	if in.SupersededBy != nil {
		value := *in.SupersededBy
		in.SupersededBy = &value
	}
	return in
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// longTermIdentityChanged reports whether a kept profile row's kind, content,
// or tags differ from the draft. Other field edits are not a supersession.
func longTermIdentityChanged(current LongTermMemory, draft MemoryDraft) bool {
	return current.Kind != draft.Kind || current.Content != draft.Content || !sameStrings(current.Tags, draft.Tags)
}

// RankMemories applies the required equal 50/50 weight. Recency decays with a
// 90-day half-life and usage is normalized within the user's active profile;
// logarithms prevent one heavily called row from permanently starving others.
func RankMemories(memories []LongTermMemory, now time.Time) []LongTermMemory {
	maxCalls := int64(0)
	for _, memory := range memories {
		if memory.CallCount > maxCalls {
			maxCalls = memory.CallCount
		}
	}
	denominator := math.Log1p(float64(maxCalls))
	result := make([]LongTermMemory, 0, len(memories))
	for _, memory := range memories {
		if memory.State != "" && memory.State != memoryStateActive {
			continue
		}
		if memory.ExpiresAt != nil && !memory.ExpiresAt.After(now) {
			continue
		}
		age := max(now.Sub(memory.LastSeenAt), 0)
		recency := math.Exp(-math.Ln2 * age.Hours() / (24 * 90))
		usage := 0.0
		if denominator > 0 {
			usage = math.Log1p(float64(memory.CallCount)) / denominator
		}
		memory.Score = 0.5*recency + 0.5*usage
		result = append(result, memory)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		if result[i].Importance != result[j].Importance {
			return result[i].Importance > result[j].Importance
		}
		if !result[i].LastSeenAt.Equal(result[j].LastSeenAt) {
			return result[i].LastSeenAt.After(result[j].LastSeenAt)
		}
		return cmp.Compare(result[i].ID, result[j].ID) < 0
	})
	return result
}
