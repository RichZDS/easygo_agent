// Package tools 包含模板固定的 Eino Tool 示例。
package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"go.uber.org/zap"
)

// CalculatorInput 表示一次确定性算术运算。
type CalculatorInput struct {
	Operation string  `json:"operation" jsonschema:"required,description=Operation to perform,enum=add,enum=subtract,enum=multiply,enum=divide"`
	A         float64 `json:"a" jsonschema:"required,description=Left operand"`
	B         float64 `json:"b" jsonschema:"required,description=Right operand"`
}

// CalculatorOutput 包含算术结果。
type CalculatorOutput struct {
	Result float64 `json:"result"`
}

// NewCalculator 构造模板内置的 Eino Calculator Tool。
func NewCalculator(logger *zap.Logger) (tool.InvokableTool, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	// calculate 执行一次受支持的算术运算。
	calculate := func(_ context.Context, input CalculatorInput) (CalculatorOutput, error) {
		var result float64
		switch input.Operation {
		case "add":
			result = input.A + input.B
		case "subtract":
			result = input.A - input.B
		case "multiply":
			result = input.A * input.B
		case "divide":
			if input.B == 0 {
				err := errors.New("cannot divide by zero")
				logger.Error("calculator invocation failed", zap.String("operation", input.Operation), zap.Error(err))
				return CalculatorOutput{}, err
			}
			result = input.A / input.B
		default:
			err := fmt.Errorf("unsupported calculator operation %q", input.Operation)
			logger.Error("calculator invocation failed", zap.String("operation", input.Operation), zap.Error(err))
			return CalculatorOutput{}, err
		}
		return CalculatorOutput{Result: result}, nil
	}

	calculator, err := utils.InferTool(
		"calculator",
		"Perform deterministic addition, subtraction, multiplication, or division.",
		calculate,
	)
	if err != nil {
		wrappedErr := fmt.Errorf("infer calculator tool schema: %w", err)
		logger.Error("create calculator tool failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return calculator, nil
}
