package tools

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"
)

// TestCalculator 通过 Eino InvokableTool 接口校验全部受支持运算。
func TestCalculator(t *testing.T) {
	t.Parallel()

	calculator, err := NewCalculator(zap.NewNop())
	if err != nil {
		t.Fatalf("NewCalculator() error = %v", err)
	}
	tests := []struct {
		name      string
		arguments string
		want      float64
		wantErr   bool
	}{
		{name: "add", arguments: `{"operation":"add","a":2,"b":3}`, want: 5},
		{name: "subtract", arguments: `{"operation":"subtract","a":7,"b":2}`, want: 5},
		{name: "multiply", arguments: `{"operation":"multiply","a":4,"b":3}`, want: 12},
		{name: "divide", arguments: `{"operation":"divide","a":8,"b":2}`, want: 4},
		{name: "divide by zero", arguments: `{"operation":"divide","a":8,"b":0}`, wantErr: true},
		{name: "unknown operation", arguments: `{"operation":"pow","a":2,"b":3}`, wantErr: true},
	}

	for _, test := range tests {
		test := test
		// runCase 校验一次 Calculator 调用。
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			outputJSON, err := calculator.InvokableRun(context.Background(), test.arguments)
			if test.wantErr {
				if err == nil {
					t.Fatal("InvokableRun() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("InvokableRun() error = %v", err)
			}
			var output CalculatorOutput
			if err := json.Unmarshal([]byte(outputJSON), &output); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if output.Result != test.want {
				t.Errorf("Result = %v, want %v", output.Result, test.want)
			}
		})
	}
}

// TestCalculatorInfo 验证 Tool 暴露稳定名称。
func TestCalculatorInfo(t *testing.T) {
	t.Parallel()

	calculator, err := NewCalculator(zap.NewNop())
	if err != nil {
		t.Fatalf("NewCalculator() error = %v", err)
	}
	info, err := calculator.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if info.Name != "calculator" {
		t.Errorf("Info().Name = %q, want calculator", info.Name)
	}
}
