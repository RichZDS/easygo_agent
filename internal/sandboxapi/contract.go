// Package sandboxapi is the Docker-free HTTP contract for the sandbox controller.
package sandboxapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	SessionHeader    = "X-EasyGo-Session-ID"
	RunHeader        = "X-EasyGo-Run-ID"
	ApplicationsPath = "/v1/applications"
	DefaultMaxBody   = 4 << 20
)

var ApplicationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type ErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type Error struct {
	StatusCode int
	Code       string
	Message    string
	RetryAfter string
}

func (err *Error) Error() string {
	message := strings.TrimSpace(err.Message)
	if message == "" {
		message = http.StatusText(err.StatusCode)
	}
	if err.Code != "" {
		message = err.Code + ": " + message
	}
	if err.RetryAfter != "" {
		message += " (retry after " + err.RetryAfter + ")"
	}
	return fmt.Sprintf("sandbox controller returned %d: %s", err.StatusCode, message)
}

func ApplicationPath(applicationID, suffix string) (string, error) {
	applicationID = strings.TrimSpace(applicationID)
	if !ApplicationIDPattern.MatchString(applicationID) {
		return "", errors.New("sandbox application_id must contain 1 to 128 letters, digits, dots, underscores, or hyphens")
	}
	return ApplicationsPath + "/" + url.PathEscape(applicationID) + suffix, nil
}

type Client struct {
	BaseURL      string
	Token        string
	HTTP         *http.Client
	MaxBodyBytes int
}

func (client *Client) Do(ctx context.Context, method, path, sessionID, runID string, body, output any) error {
	if client == nil {
		return errors.New("sandbox controller client is required")
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(runID) == "" {
		return errors.New("sandbox tool requires trusted session and run identity")
	}
	httpClient := client.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	maxBody := client.MaxBodyBytes
	if maxBody < 1 {
		maxBody = DefaultMaxBody
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode sandbox controller request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(client.BaseURL, "/")+path, reader)
	if err != nil {
		return fmt.Errorf("create sandbox controller request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+client.Token)
	request.Header.Set(SessionHeader, sessionID)
	request.Header.Set(RunHeader, runID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call sandbox controller: %w", err)
	}
	defer response.Body.Close()
	limit := int64(maxBody)
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return fmt.Errorf("read sandbox controller response: %w", err)
	}
	if int64(len(responseBody)) > limit {
		return errors.New("sandbox controller response exceeds configured output limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var envelope ErrorEnvelope
		_ = json.Unmarshal(responseBody, &envelope)
		message := strings.TrimSpace(envelope.Error.Message)
		if message == "" {
			message = strings.TrimSpace(string(responseBody))
		}
		return &Error{
			StatusCode: response.StatusCode,
			Code:       strings.TrimSpace(envelope.Error.Code),
			Message:    message,
			RetryAfter: strings.TrimSpace(response.Header.Get("Retry-After")),
		}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if len(responseBody) == 0 {
		return errors.New("sandbox controller returned an empty response")
	}
	if err := json.Unmarshal(responseBody, output); err != nil {
		return fmt.Errorf("decode sandbox controller response: %w", err)
	}
	return nil
}
