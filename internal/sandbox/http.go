package sandbox

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const (
	sessionHeader = "X-EasyGo-Session-ID"
	runHeader     = "X-EasyGo-Run-ID"
	maxHTTPBody   = int64(8 << 20)
)

var applicationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type HTTPHandler struct {
	token   string
	manager *Manager
}

func NewHTTPHandler(token string, manager *Manager) (http.Handler, error) {
	if len(token) < 32 || strings.IndexFunc(token, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return nil, errors.New("controller bearer token must contain at least 32 printable ASCII characters without spaces")
	}
	if manager == nil {
		return nil, errors.New("sandbox manager is required")
	}
	return &HTTPHandler{token: token, manager: manager}, nil
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.Path == "/healthz" {
		if request.Method != http.MethodGet {
			handler.writeError(response, &Error{Code: CodeInvalidRequest, Message: "method not allowed"}, http.StatusMethodNotAllowed)
			return
		}
		handler.writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if !strings.HasPrefix(request.URL.Path, "/v1/") {
		handler.writeError(response, notFoundError(), http.StatusNotFound)
		return
	}
	if !handler.authorized(request) {
		response.Header().Set("WWW-Authenticate", `Bearer realm="easygo-sandbox"`)
		handler.writeError(response, &Error{Code: CodeUnauthorized, Message: "valid bearer token required"}, http.StatusUnauthorized)
		return
	}
	identity := Identity{SessionID: request.Header.Get(sessionHeader), RunID: request.Header.Get(runHeader)}
	if err := validateIdentity(identity, true); err != nil {
		handler.writeError(response, err, http.StatusBadRequest)
		return
	}
	if request.URL.Path == "/v1/applications" {
		handler.handleCollection(response, request, identity)
		return
	}
	prefix := "/v1/applications/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		handler.writeError(response, notFoundError(), http.StatusNotFound)
		return
	}
	remainder := strings.TrimPrefix(request.URL.Path, prefix)
	parts := strings.Split(remainder, "/")
	if len(parts) == 0 || !applicationIDPattern.MatchString(parts[0]) {
		handler.writeError(response, invalidRequest("invalid application id"), http.StatusBadRequest)
		return
	}
	handler.handleApplication(response, request, identity, parts[0], parts[1:])
}

func (handler *HTTPHandler) handleCollection(response http.ResponseWriter, request *http.Request, identity Identity) {
	if request.Method != http.MethodPost {
		handler.writeError(response, invalidRequest("method not allowed"), http.StatusMethodNotAllowed)
		return
	}
	if err := decodeRequest(request, &struct{}{}); err != nil {
		handler.writeError(response, err, http.StatusBadRequest)
		return
	}
	result, err := handler.manager.Apply(request.Context(), identity)
	if err != nil {
		handler.writeManagerError(response, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	handler.writeJSON(response, status, result)
}

func (handler *HTTPHandler) handleApplication(response http.ResponseWriter, request *http.Request, identity Identity, applicationID string, suffix []string) {
	if len(suffix) == 0 {
		switch request.Method {
		case http.MethodGet:
			result, err := handler.manager.Status(request.Context(), identity, applicationID)
			if err != nil {
				handler.writeManagerError(response, err)
				return
			}
			handler.writeJSON(response, http.StatusOK, result)
		case http.MethodDelete:
			if err := handler.manager.Destroy(request.Context(), identity, applicationID); err != nil {
				handler.writeManagerError(response, err)
				return
			}
			response.WriteHeader(http.StatusNoContent)
		default:
			handler.writeError(response, invalidRequest("method not allowed"), http.StatusMethodNotAllowed)
		}
		return
	}
	if request.Method != http.MethodPost {
		handler.writeError(response, invalidRequest("method not allowed"), http.StatusMethodNotAllowed)
		return
	}
	switch strings.Join(suffix, "/") {
	case "create":
		if err := decodeRequest(request, &struct{}{}); err != nil {
			handler.writeError(response, err, http.StatusBadRequest)
			return
		}
		result, err := handler.manager.Create(request.Context(), identity, applicationID)
		if err != nil {
			handler.writeManagerError(response, err)
			return
		}
		handler.writeJSON(response, http.StatusOK, result)
	case "exec":
		var input ExecRequest
		if err := decodeRequest(request, &input); err != nil {
			handler.writeError(response, err, http.StatusBadRequest)
			return
		}
		result, err := handler.manager.Exec(request.Context(), identity, applicationID, input)
		if err != nil {
			handler.writeManagerError(response, err)
			return
		}
		handler.writeJSON(response, http.StatusOK, result)
	case "files/read":
		var input ReadFileRequest
		if err := decodeRequest(request, &input); err != nil {
			handler.writeError(response, err, http.StatusBadRequest)
			return
		}
		result, err := handler.manager.ReadFile(request.Context(), identity, applicationID, input)
		if err != nil {
			handler.writeManagerError(response, err)
			return
		}
		handler.writeJSON(response, http.StatusOK, result)
	case "files/write":
		var input WriteFileRequest
		if err := decodeRequest(request, &input); err != nil {
			handler.writeError(response, err, http.StatusBadRequest)
			return
		}
		result, err := handler.manager.WriteFile(request.Context(), identity, applicationID, input)
		if err != nil {
			handler.writeManagerError(response, err)
			return
		}
		handler.writeJSON(response, http.StatusOK, result)
	case "release":
		if err := decodeRequest(request, &struct{}{}); err != nil {
			handler.writeError(response, err, http.StatusBadRequest)
			return
		}
		result, err := handler.manager.Release(request.Context(), identity, applicationID)
		if err != nil {
			handler.writeManagerError(response, err)
			return
		}
		handler.writeJSON(response, http.StatusOK, result)
	default:
		handler.writeError(response, notFoundError(), http.StatusNotFound)
	}
}

func (handler *HTTPHandler) authorized(request *http.Request) bool {
	value := request.Header.Get("Authorization")
	want := "Bearer " + handler.token
	return len(value) == len(want) && subtle.ConstantTimeCompare([]byte(value), []byte(want)) == 1
}

func decodeRequest(request *http.Request, output any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return invalidRequest("Content-Type must be application/json")
	}
	reader := http.MaxBytesReader(nil, request.Body, maxHTTPBody)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return invalidRequest("invalid JSON request: " + err.Error())
	}
	var trailer any
	if err := decoder.Decode(&trailer); !errors.Is(err, io.EOF) {
		return invalidRequest("request must contain one JSON object")
	}
	return nil
}

func (handler *HTTPHandler) writeManagerError(response http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var sandboxErr *Error
	if errors.As(err, &sandboxErr) {
		switch sandboxErr.Code {
		case CodeInvalidRequest:
			status = http.StatusBadRequest
		case CodeUnauthorized:
			status = http.StatusUnauthorized
		case CodeApplicationAbsent:
			status = http.StatusNotFound
		case CodeApplicationBusy:
			status = http.StatusConflict
		case CodeCapacityExhausted, CodeApplicationLimit:
			status = http.StatusTooManyRequests
		case CodeStoragePressure:
			status = http.StatusInsufficientStorage
			// This fixed, non-sensitive message reaches the Controller's stderr in
			// addition to the structured Agent error, making the safety waterline
			// visible to Compose/host log monitoring.
			log.Printf("sandbox-controller storage pressure: %s", sandboxErr.Message)
		case CodeExpired:
			status = http.StatusGone
		case CodeCommandTimeout:
			status = http.StatusGatewayTimeout
		case CodeCanceled:
			status = 499
		}
		if sandboxErr.RetryAfter > 0 {
			seconds := int64((sandboxErr.RetryAfter + 999_999_999) / 1_000_000_000)
			response.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		}
	}
	handler.writeError(response, err, status)
}

func (handler *HTTPHandler) writeError(response http.ResponseWriter, err error, status int) {
	code, message := errorCode(err), "internal sandbox controller error"
	var sandboxErr *Error
	if errors.As(err, &sandboxErr) {
		message = sandboxErr.Message
	}
	handler.writeJSON(response, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (handler *HTTPHandler) writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		_ = fmt.Errorf("encode HTTP response: %w", err)
	}
}
