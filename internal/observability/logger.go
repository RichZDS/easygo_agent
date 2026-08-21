// Package observability constructs logging and tracing for the template.
package observability

import (
	"fmt"

	"go.uber.org/zap"
)

// NewLogger constructs the project's production Zap logger.
func NewLogger() (*zap.Logger, error) {
	logger, err := zap.NewProduction()
	if err != nil {
		wrappedErr := fmt.Errorf("create production logger: %w", err)
		zap.NewNop().Error("create logger failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return logger, nil
}
