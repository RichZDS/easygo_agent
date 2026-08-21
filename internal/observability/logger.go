// Package observability 为模板构造日志与 tracing。
package observability

import (
	"fmt"

	"go.uber.org/zap"
)

// NewLogger 构造项目使用的生产级 Zap logger。
func NewLogger() (*zap.Logger, error) {
	logger, err := zap.NewProduction()
	if err != nil {
		wrappedErr := fmt.Errorf("create production logger: %w", err)
		zap.NewNop().Error("create logger failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return logger, nil
}
