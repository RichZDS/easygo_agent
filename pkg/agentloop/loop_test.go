package agentloop

import (
	"context"
	"easygo-agent/services/ai-gateway/ai"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

type clientFunc func(context.Context, ai.Request, func(ai.Event) error) (ai.Response, error)

func (f clientFunc) Complete(c context.Context, r ai.Request, e func(ai.Event) error) (ai.Response, error) {
	return f(c, r, e)
}
func reply(blocks ...ai.Block) ai.Response {
	return ai.Response{Message: ai.Message{Role: "assistant", Content: blocks}}
}
func call(id string) ai.Block {
	return ai.Block{Type: "tool_call", ID: id, Name: "echo", Arguments: json.RawMessage(`{"value":1}`)}
}
func echo(f func(context.Context, ai.Block) (string, error)) Tool {
	return Tool{Definition: ai.Tool{Name: "echo", Parameters: json.RawMessage(`{"type":"object"}`)}, Execute: f}
}
func TestLoopBarriersStreamingAndTwoTools(t *testing.T) {
	var order []string
	var calls int
	cfg := Config{MaxSteps: 3, Tools: []Tool{echo(func(_ context.Context, b ai.Block) (string, error) {
		order = append(order, "execute:"+b.ID)
		return b.ID, nil
	})}, Commit: func(_ context.Context, m ai.Message) error { order = append(order, "commit:"+m.Role); return nil }}
	cfg.Client = clientFunc(func(_ context.Context, r ai.Request, emit func(ai.Event) error) (ai.Response, error) {
		calls++
		order = append(order, "model")
		if calls == 1 {
			return reply(call("a"), call("b")), nil
		}
		if len(r.Messages) != 4 || r.Messages[2].Content[0].Text != "a" || r.Messages[3].Content[0].ID != "b" {
			t.Fatalf("lost results: %+v", r.Messages)
		}
		if err := emit(ai.Event{Type: "text_delta", Delta: "done"}); err != nil {
			return ai.Response{}, err
		}
		return reply(ai.Block{Type: "text", Text: "done"}), nil
	})
	l, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	result, err := l.Run(context.Background(), ai.Request{Messages: []ai.Message{{Role: "user"}}}, func(e Event) error { events = append(events, e.Type); return nil })
	if err != nil || result.Steps != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	want := []string{"model", "commit:assistant", "execute:a", "commit:tool", "execute:b", "commit:tool", "model", "commit:assistant"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order=%v", order)
	}
	if !reflect.DeepEqual(events, []string{"assistant", "tool_started", "tool_finished", "tool_started", "tool_finished", "model_delta", "assistant"}) {
		t.Fatal(events)
	}
}
func TestCommitFailureStopsSideEffects(t *testing.T) {
	for _, failRole := range []string{"assistant", "tool"} {
		t.Run(failRole, func(t *testing.T) {
			models, tools := 0, 0
			failure := errors.New("durability failed")
			l, _ := New(Config{MaxSteps: 2, Client: clientFunc(func(context.Context, ai.Request, func(ai.Event) error) (ai.Response, error) {
				models++
				return reply(call("a"), call("b")), nil
			}), Tools: []Tool{echo(func(context.Context, ai.Block) (string, error) { tools++; return "ok", nil })}, Commit: func(_ context.Context, m ai.Message) error {
				if m.Role == failRole {
					return failure
				}
				return nil
			}})
			_, err := l.Run(context.Background(), ai.Request{}, nil)
			wantTools := 0
			if failRole == "tool" {
				wantTools = 1
			}
			if !errors.Is(err, failure) || models != 1 || tools != wantTools {
				t.Fatalf("err=%v models=%d tools=%d", err, models, tools)
			}
		})
	}
}
func TestRecoverableToolResults(t *testing.T) {
	for _, kind := range []string{"unknown", "malformed", "error"} {
		t.Run(kind, func(t *testing.T) {
			n := 0
			l, _ := New(Config{MaxSteps: 2, Tools: []Tool{echo(func(context.Context, ai.Block) (string, error) { return "", errors.New("broken tool") })}, Client: clientFunc(func(_ context.Context, r ai.Request, _ func(ai.Event) error) (ai.Response, error) {
				n++
				if n == 1 {
					c := call("a")
					if kind == "unknown" {
						c.Name = "missing"
					}
					if kind == "malformed" {
						c.Arguments = json.RawMessage(`{`)
					}
					return reply(c), nil
				}
				b := r.Messages[len(r.Messages)-1].Content[0]
				if !b.IsError || b.Type != "tool_result" || b.ID != "a" {
					t.Fatalf("not explicit error result: %+v", b)
				}
				return reply(ai.Block{Type: "text", Text: "recovered"}), nil
			})})
			if _, err := l.Run(context.Background(), ai.Request{}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestFinalSynthesisDisablesTools(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		t.Run(map[bool]string{false: "synthesis", true: "refuses"}[repeat], func(t *testing.T) {
			models, tools := 0, 0
			l, _ := New(Config{MaxSteps: 1, Tools: []Tool{echo(func(context.Context, ai.Block) (string, error) { tools++; return "evidence", nil })}, BeforeModel: func(_ context.Context, r ai.Request) (ai.Request, error) {
				r.Tools = []ai.Tool{{Name: "echo"}}
				return r, nil
			}, Client: clientFunc(func(_ context.Context, r ai.Request, _ func(ai.Event) error) (ai.Response, error) {
				models++
				if models == 1 {
					return reply(call("a")), nil
				}
				if len(r.Tools) != 0 || r.ToolChoice != "none" {
					t.Fatalf("tools enabled in synthesis: %+v", r)
				}
				if repeat {
					return reply(call("b")), nil
				}
				return reply(ai.Block{Type: "text", Text: "final"}), nil
			})})
			result, err := l.Run(context.Background(), ai.Request{}, nil)
			if repeat && !errors.Is(err, ErrStepLimit) {
				t.Fatal(err)
			}
			if !repeat && (err != nil || result.Response.Message.Content[0].Text != "final") {
				t.Fatalf("%+v %v", result, err)
			}
			if models != 2 || tools != 1 {
				t.Fatalf("models=%d tools=%d", models, tools)
			}
		})
	}
}
func TestCancellationStreamErrorAndDeadline(t *testing.T) {
	for _, kind := range []string{"canceled", "stream", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sideEffects := 0
			failure := errors.New("broken stream")
			cfg := Config{MaxSteps: 2, Tools: []Tool{echo(func(context.Context, ai.Block) (string, error) { sideEffects++; return "", nil })}, Client: clientFunc(func(ctx context.Context, _ ai.Request, emit func(ai.Event) error) (ai.Response, error) {
				if kind == "deadline" {
					<-ctx.Done()
					return ai.Response{}, ctx.Err()
				}
				if err := emit(ai.Event{Type: "text_delta", Delta: "partial"}); err != nil {
					return ai.Response{}, err
				}
				if kind == "stream" {
					return ai.Response{}, failure
				}
				cancel()
				return reply(call("a")), nil
			})}
			if kind == "deadline" {
				cfg.MaxDuration = time.Millisecond
			}
			l, _ := New(cfg)
			_, err := l.Run(ctx, ai.Request{}, func(Event) error { return nil })
			if err == nil || sideEffects != 0 {
				t.Fatalf("err=%v sideEffects=%d", err, sideEffects)
			}
		})
	}
}
func TestSteeringBetweenToolsAndTurns(t *testing.T) {
	polls, n := 0, 0
	l, _ := New(Config{MaxSteps: 3, Tools: []Tool{echo(func(context.Context, ai.Block) (string, error) { return "ok", nil })}, Steering: func(context.Context) ([]ai.Message, error) {
		polls++
		if polls == 1 {
			return []ai.Message{{Role: "user", Content: []ai.Block{{Type: "text", Text: "change"}}}}, nil
		}
		return nil, nil
	}, Client: clientFunc(func(_ context.Context, r ai.Request, _ func(ai.Event) error) (ai.Response, error) {
		n++
		if n == 1 {
			return reply(call("a"), call("b")), nil
		}
		if r.Messages[3].Content[0].Text != "change" {
			t.Fatalf("missing steering: %+v", r.Messages)
		}
		return reply(ai.Block{Type: "text", Text: "done"}), nil
	})})
	if _, err := l.Run(context.Background(), ai.Request{}, nil); err != nil {
		t.Fatal(err)
	}
	if polls != 3 {
		t.Fatal(polls)
	}
}

func TestOpaqueProviderStateIsOwnedAndRetained(t *testing.T) {
	raw := json.RawMessage(`{"protocol":"anthropic","value":"opaque"}`)
	original := append(json.RawMessage(nil), raw...)
	messages := []ai.Message{{Role: "assistant", Content: []ai.Block{{Type: "reasoning", Text: "reason", ProviderState: raw}}}}
	l, _ := New(Config{MaxSteps: 1, Client: clientFunc(func(_ context.Context, r ai.Request, _ func(ai.Event) error) (ai.Response, error) {
		raw[0] = '!'
		if string(r.Messages[0].Content[0].ProviderState) != string(original) {
			t.Fatal("caller state aliases run history")
		}
		return reply(ai.Block{Type: "text", Text: "done"}), nil
	})})
	result, err := l.Run(context.Background(), ai.Request{Messages: messages}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Messages[0].Content[0].ProviderState) != string(original) {
		t.Fatal("opaque state lost")
	}
}
