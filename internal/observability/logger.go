// Package observability 为模板构造日志。
package observability

import (
	"fmt"

	"easygo-agent/internal/logger"

	"go.uber.org/zap"
)

// NewLogger 构造项目使用的生产级 Zap logger。
func NewLogger() (*zap.Logger, error) {
	produced, err := zap.NewProduction()
	if err != nil {
		wrappedErr := fmt.Errorf("create production logger: %w", err)
		logger.Error("create logger failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	zap.ReplaceGlobals(produced)
	return produced, nil
}
