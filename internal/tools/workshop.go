package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	agentruntime "easygo-agent/internal/agent/runtime"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// WorkshopConfig connects tools to an operator-managed workshop service.
// The model cannot select endpoints, credentials, workflow commands or owners.
type WorkshopConfig struct {
	BaseURL        string
	AuthToken      string
	RequestTimeout time.Duration
}

type workshopTransport struct {
	base   string
	token  string
	client *http.Client
}

func workshopNamespace(ctx context.Context, mutate bool) (string, error) {
	i, ok := agentruntime.InvocationIdentityFromContext(ctx)
	if !ok || i.Username == "" {
		return "", errors.New("trusted workshop user and session identity is unavailable")
	}
	if mutate && i.Internal {
		return "", errors.New("internal notifications may only query workshop tasks")
	}
	b, _ := json.Marshal([]string{i.Username, i.SessionID})
	return fmt.Sprintf("easygo-%x", sha256.Sum256(b)), nil
}

func newWorkshopTransport(cfg WorkshopConfig) (*workshopTransport, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("workshop requires an HTTP base URL without credentials, query or fragment")
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 15 * time.Second
	}
	return &workshopTransport{base: strings.TrimRight(cfg.BaseURL, "/"), token: cfg.AuthToken, client: &http.Client{
		Timeout:       cfg.RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *workshopTransport) call(ctx context.Context, method, path, namespace string, body any, out any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	endpoint, err := url.Parse(c.base + path)
	if err != nil {
		return errors.New("invalid workshop request")
	}
	query := endpoint.Query()
	query.Set("namespace", namespace)
	query.Set("view", "summary")
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid workshop request")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("workshop service request failed")
	}
	defer resp.Body.Close()
	const limit = 1 << 20
	data, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(data) > limit {
		return errors.New("workshop response unreadable or too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("workshop request failed (HTTP %d)", resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("invalid workshop JSON response")
	}
	return nil
}

// workshopTaskView deliberately omits the local filesystem path, native session
// IDs, and workflow instructions. Artifacts remain relative manifest entries.
type workshopTaskView struct {
	ID        string `json:"id"`
	Namespace string `json:"namespace,omitempty"`
	Status    string `json:"status"`
	RunCount  int    `json:"run_count"`
	Runs      []struct {
		ID                 string `json:"id"`
		Status             string `json:"status"`
		Text               string `json:"text,omitempty"`
		Error              string `json:"error,omitempty"`
		ErrorTruncated     bool   `json:"error_truncated"`
		TextBytes          int    `json:"text_bytes"`
		TextTruncated      bool   `json:"text_truncated"`
		ArtifactCount      int    `json:"artifact_count"`
		ArtifactsTruncated bool   `json:"artifacts_truncated"`
		Artifacts          []struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"artifacts,omitempty"`
	} `json:"runs,omitempty"`
}

func (v *workshopTaskView) scoped(namespace string) error {
	if v.ID == "" || v.Namespace != namespace {
		return errors.New("workshop task ownership mismatch")
	}
	v.Namespace = ""
	return nil
}

func workshopTaskPath(id string) (string, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, "/\\?#%") || id == "." || id == ".." {
		return "", errors.New("invalid workshop task id")
	}
	return "/v1/tasks/" + url.PathEscape(id), nil
}

type workshopWorkflowView struct {
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	Engine         string   `json:"engine"`
	Model          string   `json:"model"`
	Policy         string   `json:"policy"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Artifacts      []string `json:"artifacts,omitempty"`
}

func NewWorkshopTools(cfg WorkshopConfig) ([]tool.BaseTool, error) {
	c, err := newWorkshopTransport(cfg)
	if err != nil {
		return nil, err
	}
	catalog, err := utils.InferTool("workshop_catalog", "Discover configured CLI workflow names and their runtime policies before submitting a workshop task.", func(ctx context.Context, _ struct{}) ([]workshopWorkflowView, error) {
		ns, err := workshopNamespace(ctx, false)
		if err != nil {
			return nil, err
		}
		var out []workshopWorkflowView
		err = c.call(ctx, http.MethodGet, "/v1/workflows", ns, nil, &out)
		return out, err
	})
	if err != nil {
		return nil, err
	}
	submit, err := utils.InferTool("workshop_submit", "Submit a configured CLI workflow as background work. Supply a stable idempotency_key for retries. Acceptance is not completion; use workshop_get to inspect results.", func(ctx context.Context, in struct {
		Workflow       string `json:"workflow" jsonschema:"required"`
		Input          string `json:"input" jsonschema:"required"`
		IdempotencyKey string `json:"idempotency_key" jsonschema:"required"`
	}) (workshopTaskView, error) {
		ns, err := workshopNamespace(ctx, true)
		if err != nil {
			return workshopTaskView{}, err
		}
		if strings.TrimSpace(in.IdempotencyKey) == "" {
			return workshopTaskView{}, errors.New("idempotency_key is required")
		}
		var out workshopTaskView
		err = c.call(ctx, http.MethodPost, "/v1/tasks", ns, map[string]string{"workflow": in.Workflow, "input": in.Input, "idempotency_key": in.IdempotencyKey}, &out)
		if err == nil {
			err = out.scoped(ns)
		}
		return out, err
	})
	if err != nil {
		return nil, err
	}
	get, err := utils.InferTool("workshop_get", "Get a bounded summary of a workshop task owned by this conversation. Includes the latest attempt, total run count and explicit result/artifact truncation flags. Read full text with workshop_result.", func(ctx context.Context, in taskIDInput) (workshopTaskView, error) {
		ns, err := workshopNamespace(ctx, false)
		if err != nil {
			return workshopTaskView{}, err
		}
		path, err := workshopTaskPath(in.TaskID)
		if err != nil {
			return workshopTaskView{}, err
		}
		var out workshopTaskView
		err = c.call(ctx, http.MethodGet, path, ns, nil, &out)
		if err == nil {
			err = out.scoped(ns)
		}
		return out, err
	})
	if err != nil {
		return nil, err
	}
	list, err := utils.InferTool("workshop_list", "List a page of workshop task metadata owned by this conversation. No result text or artifact arrays are returned; use workshop_get and workshop_result. next_offset is null at the end.", func(ctx context.Context, in workshopPageInput) (workshopListPage, error) {
		ns, err := workshopNamespace(ctx, false)
		if err != nil {
			return workshopListPage{}, err
		}
		var out workshopListPage
		if err = c.call(ctx, http.MethodGet, pagePath("/v1/tasks", in, ""), ns, nil, &out); err != nil {
			return workshopListPage{}, err
		}
		for i := range out.Tasks {
			if out.Tasks[i].ID == "" || out.Tasks[i].Namespace != ns {
				return workshopListPage{}, errors.New("workshop task ownership mismatch")
			}
			out.Tasks[i].Namespace = ""
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	result, err := utils.InferTool("workshop_result", "Read a bounded page of result text for an owned workshop task. Pin run_id from the first response for later pages. offset is a UTF-8 byte cursor; follow next_offset until eof. limit defaults to 8192 bytes, allowed 4..32768.", func(ctx context.Context, in struct {
		TaskID string `json:"task_id" jsonschema:"required"`
		RunID  string `json:"run_id,omitempty"`
		Offset *int   `json:"offset,omitempty"`
		Limit  *int   `json:"limit,omitempty"`
	}) (workshopResultPage, error) {
		ns, err := workshopNamespace(ctx, false)
		if err != nil {
			return workshopResultPage{}, err
		}
		path, err := workshopTaskPath(in.TaskID)
		if err != nil {
			return workshopResultPage{}, err
		}
		var out workshopResultPage
		if err = c.call(ctx, http.MethodGet, pagePath(path+"/result", workshopPageInput{Offset: in.Offset, Limit: in.Limit}, in.RunID), ns, nil, &out); err != nil {
			return workshopResultPage{}, err
		}
		if out.TaskID != in.TaskID || out.RunID == "" || (in.RunID != "" && out.RunID != in.RunID) {
			return workshopResultPage{}, errors.New("workshop result identity mismatch")
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	cancel, err := utils.InferTool("workshop_cancel", "Cancel a CLI workshop task owned by this conversation.", func(ctx context.Context, in taskIDInput) (workshopTaskView, error) {
		ns, err := workshopNamespace(ctx, true)
		if err != nil {
			return workshopTaskView{}, err
		}
		path, err := workshopTaskPath(in.TaskID)
		if err != nil {
			return workshopTaskView{}, err
		}
		var out workshopTaskView
		err = c.call(ctx, http.MethodPost, path+"/cancel", ns, nil, &out)
		if err == nil {
			err = out.scoped(ns)
		}
		return out, err
	})
	if err != nil {
		return nil, err
	}
	resume, err := utils.InferTool("workshop_resume", "Continue a terminal CLI task in its original workspace and native session with new instructions. Creates another attempt; do not retry automatically after an uncertain response.", func(ctx context.Context, in struct {
		TaskID string `json:"task_id" jsonschema:"required"`
		Input  string `json:"input" jsonschema:"required"`
	}) (workshopTaskView, error) {
		ns, err := workshopNamespace(ctx, true)
		if err != nil {
			return workshopTaskView{}, err
		}
		path, err := workshopTaskPath(in.TaskID)
		if err != nil {
			return workshopTaskView{}, err
		}
		var out workshopTaskView
		err = c.call(ctx, http.MethodPost, path+"/resume", ns, map[string]string{"input": in.Input}, &out)
		if err == nil {
			err = out.scoped(ns)
		}
		return out, err
	})
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{catalog, submit, get, list, result, cancel, resume}, nil
}

// Page cursors are separate from ownership: call always overwrites namespace.
type workshopPageInput struct {
	Offset *int `json:"offset,omitempty"`
	Limit  *int `json:"limit,omitempty"`
}

type workshopListPage struct {
	Tasks []struct {
		ID        string `json:"id"`
		Namespace string `json:"namespace,omitempty"`
		Status    string `json:"status"`
		RunCount  int    `json:"run_count"`
		Runs      []struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			TextBytes     int    `json:"text_bytes"`
			ArtifactCount int    `json:"artifact_count"`
		} `json:"runs"`
	} `json:"tasks"`
	Offset     int  `json:"offset"`
	NextOffset *int `json:"next_offset"`
}

type workshopResultPage struct {
	TaskID     string `json:"task_id"`
	RunID      string `json:"run_id"`
	Text       string `json:"text"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	TotalBytes int    `json:"total_bytes"`
	EOF        bool   `json:"eof"`
}

func pagePath(path string, in workshopPageInput, runID string) string {
	query := url.Values{}
	if in.Offset != nil {
		query.Set("offset", strconv.Itoa(*in.Offset))
	}
	if in.Limit != nil {
		query.Set("limit", strconv.Itoa(*in.Limit))
	}
	if runID != "" {
		query.Set("run_id", runID)
	}
	return path + "?" + query.Encode()
}
