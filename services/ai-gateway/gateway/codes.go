package gateway

// Code is a gateway error code. RPC callers receive it as error.data.code, so
// the strings are part of the RPC contract and must not change.
type Code string

const (
	CodeInvalidConfig           Code = "invalid_config"
	CodeMissingEnvironment      Code = "missing_environment"
	CodeUnknownModel            Code = "unknown_model"
	CodeInvalidRequest          Code = "invalid_request"
	CodeInvalidParameters       Code = "invalid_parameters"
	CodeUnsupportedCapability   Code = "unsupported_capability"
	CodeRequestTooLarge         Code = "request_too_large"
	CodeBillingIdentityRequired Code = "billing_identity_required"
	CodeBillingUnavailable      Code = "billing_unavailable"
	CodeInsufficientCredits     Code = "insufficient_credits"
	CodeDuplicateRequest        Code = "duplicate_request"
	CodeCanceled                Code = "canceled"
	CodeTransportError          Code = "transport_error"
	CodeUpstreamHTTPError       Code = "upstream_http_error"
	CodeUpstreamError           Code = "upstream_error"
	CodeInvalidResponse         Code = "invalid_response"
	CodeTruncatedStream         Code = "truncated_stream"
	CodeIncompleteResponse      Code = "incomplete_response"
	CodeRefusedResponse         Code = "refused_response"
	CodeResponseTooLarge        Code = "response_too_large"
	CodeReadError               Code = "read_error"
	CodeStreamReadError         Code = "stream_read_error"
	CodeCallbackError           Code = "callback_error"
	// CodeInternal reports an error that carries no gateway code.
	CodeInternal Code = "internal_error"
)

// codes is the single table of error codes and the JSON-RPC error each one is
// reported as. Codes whose rpcData is CodeInternal reach RPC callers only as
// internal_error.
var codes = map[Code]struct {
	rpcCode int
	rpcData Code
}{
	CodeInsufficientCredits:     {-32002, CodeInsufficientCredits},
	CodeDuplicateRequest:        {-32009, CodeDuplicateRequest},
	CodeBillingIdentityRequired: {-32003, CodeBillingIdentityRequired},
	CodeBillingUnavailable:      {-32000, CodeBillingUnavailable},
	CodeInvalidRequest:          {-32602, CodeInvalidRequest},
	CodeInvalidParameters:       {-32602, CodeInvalidParameters},
	CodeUnsupportedCapability:   {-32602, CodeUnsupportedCapability},
	CodeRequestTooLarge:         {-32602, CodeRequestTooLarge},
	CodeUnknownModel:            {-32004, CodeUnknownModel},
	CodeUpstreamHTTPError:       {-32000, CodeUpstreamHTTPError},
	CodeUpstreamError:           {-32000, CodeUpstreamError},
	CodeInvalidResponse:         {-32000, CodeInvalidResponse},
	CodeTruncatedStream:         {-32000, CodeTruncatedStream},
	CodeIncompleteResponse:      {-32000, CodeIncompleteResponse},
	CodeRefusedResponse:         {-32000, CodeRefusedResponse},
	CodeCanceled:                {-32000, CodeCanceled},
	CodeResponseTooLarge:        {-32000, CodeResponseTooLarge},
	CodeTransportError:          {-32000, CodeTransportError},
	CodeStreamReadError:         {-32000, CodeStreamReadError},
	CodeCallbackError:           {-32000, CodeCallbackError},
	CodeInvalidConfig:           {-32603, CodeInternal},
	CodeMissingEnvironment:      {-32603, CodeInternal},
	CodeReadError:               {-32603, CodeInternal},
	CodeInternal:                {-32603, CodeInternal},
}

// RPCError returns the JSON-RPC error code and error.data.code for code.
// Codes outside the table are internal errors.
func RPCError(code Code) (int, string) {
	c, ok := codes[code]
	if !ok {
		c = codes[CodeInternal]
	}
	return c.rpcCode, string(c.rpcData)
}
