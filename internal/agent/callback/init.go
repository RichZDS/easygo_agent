package callback

import (
	"context"

	"easygo-agent/internal/platform/logger"

	"github.com/cloudwego/eino/callbacks"
	"go.uber.org/zap"
)

func Init() {
	traceHandler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			logger.Info("[trace] component start", zap.String("component", string(info.Component)), zap.String("name", info.Name))
			return ctx
		}).
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			logger.Info("[trace] component end", zap.String("component", string(info.Component)), zap.String("name", info.Name))
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			logger.Error("[trace] component error", zap.String("component", string(info.Component)), zap.String("name", info.Name), zap.Error(err))
			return ctx
		}).
		Build()

	callbacks.AppendGlobalHandlers(traceHandler)
}
