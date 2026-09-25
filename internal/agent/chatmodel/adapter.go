package chatmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

	"easygo-agent/pkg/ai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Adapter is the sole Eino model boundary. Provider transport belongs to gateway.
type Adapter struct {
	client            ai.Client
	alias             string
	defaults          map[string]json.RawMessage
	streamingDisabled bool
}

func NewAdapter(client ai.Client, alias string) (*Adapter, error) {
	if client == nil || alias == "" {
		return nil, errors.New("client and model alias required")
	}
	return &Adapter{client: client, alias: alias}, nil
}

var _ model.AgenticModel = (*Adapter)(nil)

func (a *Adapter) request(messages []*schema.AgenticMessage, opts ...model.Option) (ai.Request, error) {
	r := ai.Request{Model: a.alias}
	var err error
	r.Messages, err = MessagesToAI(messages)
	if err != nil {
		return r, err
	}
	o := model.GetCommonOptions(nil, opts...)
	if o.ToolSearchTool != nil || len(o.DeferredTools) > 0 {
		return r, errors.New("server tool search and deferred tools are unsupported")
	}
	if o.Model != nil {
		r.Model = *o.Model
	}
	if o.MaxTokens != nil {
		r.MaxOutputTokens = *o.MaxTokens
	}
	if o.Temperature != nil {
		v := float64(*o.Temperature)
		r.Temperature = &v
	}
	r.Parameters = copyParameters(a.defaults)
	specific := model.GetImplSpecificOptions(&adapterOptions{}, opts...)
	for key, value := range specific.parameters {
		r.Parameters[key] = append(json.RawMessage(nil), value...)
	}
	if o.TopP != nil {
		r.Parameters["top_p"], err = json.Marshal(*o.TopP)
		if err != nil {
			return r, err
		}
	}
	if o.Stop != nil {
		r.Parameters["stop"], err = json.Marshal(o.Stop)
		if err != nil {
			return r, err
		}
	}
	choice := schema.ToolChoiceAllowed
	allowed := o.AllowedToolNames
	if o.ToolChoice != nil {
		choice = *o.ToolChoice
	}
	if o.AgenticToolChoice != nil {
		c := o.AgenticToolChoice
		choice = c.Type
		var selected []*schema.AllowedTool
		if c.Allowed != nil {
			selected = c.Allowed.Tools
		}
		if c.Forced != nil {
			selected = c.Forced.Tools
		}
		allowed = nil
		for _, s := range selected {
			if s == nil || s.FunctionName == "" || s.MCPTool != nil || s.ServerTool != nil {
				return r, errors.New("only function tool choice is supported")
			}
			allowed = append(allowed, s.FunctionName)
		}
	}
	switch choice {
	case schema.ToolChoiceAllowed:
		r.ToolChoice = "auto"
	case schema.ToolChoiceForbidden:
		r.ToolChoice = "none"
	case schema.ToolChoiceForced:
		r.ToolChoice = "required"
	default:
		return r, fmt.Errorf("unsupported tool choice %q", choice)
	}
	if o.ToolChoice == nil && o.AgenticToolChoice == nil && len(o.AllowedToolNames) == 0 && r.Parameters["tool_choice"] != nil {
		r.ToolChoice = ""
	}
	selected := o.Tools
	if len(allowed) > 0 {
		selected = nil
		seen := make(map[string]bool)
		for _, t := range o.Tools {
			if t == nil {
				return r, errors.New("nil tool definition")
			}
			for _, name := range allowed {
				if t.Name == name {
					selected = append(selected, t)
					seen[name] = true
					break
				}
			}
		}
		for _, name := range allowed {
			if !seen[name] {
				return r, fmt.Errorf("tool choice references unknown tool %q", name)
			}
		}
	}
	r.Tools, err = ToolsToAI(selected)
	return r, err
}
func (a *Adapter) Generate(ctx context.Context, messages []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	r, err := a.request(messages, opts...)
	if err != nil {
		return nil, err
	}
	response, err := a.client.Complete(ctx, r, nil)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = validateResponse(response); err != nil {
		return nil, err
	}
	return ResponseFromAI(response), nil
}
func (a *Adapter) Stream(ctx context.Context, messages []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	if a.streamingDisabled {
		// Explicit completed-response mode. Never retry a failed streaming call or
		// present a buffered response as incremental provider output.
		message, err := a.Generate(ctx, messages, opts...)
		if err != nil {
			return nil, err
		}
		return schema.StreamReaderFromArray([]*schema.AgenticMessage{message}), nil
	}

	req, err := a.request(messages, opts...)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	reader, writer := schema.Pipe[*schema.AgenticMessage](16)
	go func() {
		defer func() { writer.Close(); cancel() }()
		var chunks []*schema.AgenticMessage
		response, err := a.client.Complete(ctx, req, func(e ai.Event) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			chunk, err := DeltaFromAI(e)
			if err != nil {
				return err
			}
			chunks = append(chunks, chunk)
			if writer.Send(chunk, nil) {
				cancel()
				return io.ErrClosedPipe
			}
			return nil
		})
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = validateResponse(response)
		}
		if err != nil {
			_ = writer.Send(nil, err)
			return
		}
		message := ResponseFromAI(response)
		if len(chunks) > 0 {
			// Some providers disclose only opaque state for redacted reasoning at
			// completion. Materialize its empty block without replaying visible text.
			seen := map[int]bool{}
			for _, chunk := range chunks {
				for _, block := range chunk.ContentBlocks {
					if block.StreamingMeta != nil {
						seen[block.StreamingMeta.Index] = true
					}
				}
			}
			for index, block := range response.Message.Content {
				if !seen[index] && block.Type == "reasoning" && block.Text == "" && len(block.ProviderState) > 0 {
					chunk, _ := DeltaFromAI(ai.Event{Type: "reasoning_delta", Index: index})
					chunks = append(chunks, chunk)
					if writer.Send(chunk, nil) {
						return
					}
				}
			}
			joined, joinErr := schema.ConcatAgenticMessages(chunks)
			if joinErr != nil {
				_ = writer.Send(nil, joinErr)
				return
			}
			projected, projectionErr := MessagesToAI([]*schema.AgenticMessage{joined})
			if projectionErr != nil {
				_ = writer.Send(nil, projectionErr)
				return
			}
			if len(projected) != 1 || !sameContent(projected[0], response.Message) {
				_ = writer.Send(nil, errors.New("model stream differs from authoritative completion"))
				return
			}
			message.ContentBlocks = nil // terminal metadata only; don't duplicate streamed text
		}
		_ = writer.Send(message, nil)
	}()
	// The host must cancel its context when abandoning the stream. A closed
	// consumer is also detected on the next provider delta.
	return schema.StreamReaderWithConvert(reader, func(m *schema.AgenticMessage) (*schema.AgenticMessage, error) { return m, nil }, schema.WithOnEOF(func() (any, error) { cancel(); return nil, io.EOF }), schema.WithErrWrapper(func(err error) error { cancel(); return err })), nil
}
func sameContent(a, b ai.Message) bool {
	// JSON object whitespace in tool arguments is semantically irrelevant.
	for i := range a.Content {
		a.Content[i].Arguments = normalizeJSON(a.Content[i].Arguments)
		a.Content[i].ProviderState = nil
	}
	b.Content = append([]ai.Block(nil), b.Content...)
	for i := range b.Content {
		b.Content[i].Arguments = normalizeJSON(b.Content[i].Arguments)
		b.Content[i].ProviderState = nil
	}
	return reflect.DeepEqual(a, b)
}
func normalizeJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	out, _ := json.Marshal(v)
	return out
}
func validateResponse(r ai.Response) error {
	if r.Message.Role != "assistant" {
		return fmt.Errorf("model returned role %q", r.Message.Role)
	}
	for _, b := range r.Message.Content {
		switch b.Type {
		case "text", "reasoning", "tool_call":
		default:
			return fmt.Errorf("unsupported assistant block %q", b.Type)
		}
	}
	return nil
}

type adapterOptions struct{ parameters map[string]json.RawMessage }

// WithParameters supplies canonical request parameters. They override YAML
// defaults; explicit common model options (temperature, max tokens, etc.) win.
func WithParameters(parameters map[string]json.RawMessage) model.Option {
	owned := copyParameters(parameters)
	return model.WrapImplSpecificOptFn(func(o *adapterOptions) {
		if o.parameters == nil {
			o.parameters = make(map[string]json.RawMessage)
		}
		for k, v := range owned {
			o.parameters[k] = append(json.RawMessage(nil), v...)
		}
	})
}

func copyParameters(in map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}
