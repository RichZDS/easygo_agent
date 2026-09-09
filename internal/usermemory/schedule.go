package usermemory

import (
	"fmt"
	"strings"
	"time"
)

func parseDailyAt(value string) (int, int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, 0, fmt.Errorf("memory.daily_at must be HH:MM: %w", err)
	}
	return parsed.Hour(), parsed.Minute(), nil
}

func nextDailyRun(now time.Time, location *time.Location, hour, minute int) time.Time {
	local := now.In(location)
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, location)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
