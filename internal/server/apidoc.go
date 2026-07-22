package server

// This file defines documentation-only shapes referenced by the Swagger
// annotations on the handlers. mortise forwards request/response bodies
// verbatim, so these types exist purely to describe the OpenAI-compatible API
// in the generated OpenAPI spec — they are not used at runtime.

// ChatCompletionRequest is the OpenAI-compatible chat request. Only model,
// stream, and stream_options are inspected by mortise; any other OpenAI fields
// (temperature, max_tokens, tools, …) are forwarded unchanged.
type ChatCompletionRequest struct {
	Model         string         `json:"model" example:"llama-3.1-8b"`
	Messages      []ChatMessage  `json:"messages"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// ChatMessage is a single conversation turn.
type ChatMessage struct {
	Role    string `json:"role" example:"user" enums:"system,user,assistant,tool"`
	Content string `json:"content" example:"Hello!"`
}

// StreamOptions mirrors OpenAI's stream_options.
type StreamOptions struct {
	// IncludeUsage emits a final chunk carrying usage (needed for streaming
	// token accounting).
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatCompletionResponse is the OpenAI-compatible completion (subset shown).
type ChatCompletionResponse struct {
	ID     string      `json:"id" example:"chatcmpl-abc123"`
	Object string      `json:"object" example:"chat.completion"`
	Model  string      `json:"model" example:"llama-3.1-8b"`
	Usage  *UsageBlock `json:"usage,omitempty"`
}

// UsageBlock is the token accounting returned by backends.
type UsageBlock struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ModelList is the /v1/models response.
type ModelList struct {
	Object string     `json:"object" example:"list"`
	Data   []ModelDoc `json:"data"`
}

// ModelDoc is one entry in a ModelList.
type ModelDoc struct {
	ID      string `json:"id" example:"llama-3.1-8b"`
	Object  string `json:"object" example:"model"`
	OwnedBy string `json:"owned_by" example:"mortise"`
}

// ErrorResponse is the OpenAI-shaped error envelope mortise returns.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the inner error object.
type ErrorDetail struct {
	Message string `json:"message" example:"invalid api key"`
	Type    string `json:"type" example:"invalid_request_error"`
	Code    string `json:"code,omitempty" example:"invalid_api_key"`
}
