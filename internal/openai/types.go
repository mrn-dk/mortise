// Package openai holds the minimal subset of OpenAI-compatible request and
// response shapes that mortise needs to route and account for requests. Bodies
// are proxied largely verbatim; we only peek at the fields we care about.
package openai

import "encoding/json"

// ChatRequest is the subset of the /v1/chat/completions request body mortise
// inspects. RawExtra preserves all other fields for verbatim forwarding.
type ChatRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`

	// StreamOptions lets clients request usage stats on the final SSE chunk.
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// StreamOptions mirrors OpenAI's stream_options.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// Usage is the token accounting block returned by OpenAI-compatible backends.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse is the subset of a non-streaming response mortise reads for
// token accounting.
type ChatResponse struct {
	Usage *Usage `json:"usage"`
}

// PeekRequest parses the fields mortise needs (model, stream) from a raw
// request body, ignoring everything else. The body is still forwarded verbatim.
func PeekRequest(body []byte) (model string, stream bool, err error) {
	var r ChatRequest
	if err := json.Unmarshal(body, &r); err != nil {
		return "", false, err
	}
	return r.Model, r.Stream, nil
}

// ErrorBody is the OpenAI-shaped error envelope mortise returns to clients.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the inner error object.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// NewError builds a marshalled OpenAI error envelope.
func NewError(message, typ, code string) []byte {
	b, _ := json.Marshal(ErrorBody{Error: ErrorDetail{Message: message, Type: typ, Code: code}})
	return b
}
