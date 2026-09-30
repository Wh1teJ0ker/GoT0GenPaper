// Package llm is a thin client over the newapi gateway.
// It centralises prompt dispatch + response handling so pipeline stages
// don't each roll their own HTTP code.
//
// Supports: plain chat, JSON-mode responses, temperature control,
// and a typed helper for structured JSON output.
package llm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client wraps newapi access.
type Client struct {
	BaseURL         string
	APIKey          string
	ReasoningEffort string
	MaxTokens       int
	HTTP            *http.Client
}

// New creates a Client.
func New(baseURL, apiKey string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Some OpenAI-compatible gateways advertise HTTP/2 but leave streamed
	// response handling stalled. Keep the client on HTTP/1.1 for predictable
	// request completion across providers.
	transport.ForceAttemptHTTP2 = false
	// Preserve the standard TLS settings while advertising HTTP/1.1 only.
	// A few OpenAI-compatible gateways negotiate HTTP/2 successfully but do
	// not complete chat response streams reliably with Go's HTTP/2 client.
	tlsConfig := &tls.Config{}
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
	}
	tlsConfig.NextProtos = []string{"http/1.1"}
	transport.TLSClientConfig = tlsConfig
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{Timeout: 180 * time.Second, Transport: transport},
	}
}

// Message is a single chat message.
type Message struct {
	Role    string `json:"role"` // system / user / assistant
	Content any    `json:"content"`
}

// ContentPart is an OpenAI-compatible multimodal message part. Providers that
// do not support image_url will return an error and callers must keep the
// corresponding result in manual review.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// ChatOptions controls optional request parameters.
type ChatOptions struct {
	Temperature     *float64 // 0~2; nil = provider default
	JSONMode        bool     // request structured JSON output
	MaxTokens       int      // 0 = provider default
	ReasoningEffort string   // provider-supported reasoning level
	DisableRetries  bool     // useful for connection checks where one response is enough
}

// ChatRequest mirrors the OpenAI-compatible chat completions payload.
type ChatRequest struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	Temperature     *float64  `json:"temperature,omitempty"`
	MaxTokens       int       `json:"max_tokens,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	// ResponseFormat enforces JSON output when JSONMode=true.
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type responseFormat struct {
	Type string `json:"type"` // "json_object" for JSON mode
}

// ChatResponse is a minimal subset of the response.
type ChatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Chat sends a chat completion request and returns the assistant text.
// Transient failures — HTTP 429/5xx and network errors — are retried with
// exponential backoff (honouring Retry-After); a burst of pipeline calls
// otherwise collapses entire downstream stages into fallbacks.
func (c *Client) Chat(ctx context.Context, model string, messages []Message, opts ...ChatOptions) (string, error) {
	var opt ChatOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.MaxTokens <= 0 {
		opt.MaxTokens = c.MaxTokens
	}
	if opt.ReasoningEffort == "" {
		opt.ReasoningEffort = c.ReasoningEffort
	}

	reqBody := ChatRequest{
		Model:           model,
		Messages:        messages,
		Temperature:     opt.Temperature,
		MaxTokens:       opt.MaxTokens,
		ReasoningEffort: opt.ReasoningEffort,
	}
	if opt.JSONMode {
		reqBody.ResponseFormat = &responseFormat{Type: "json_object"}
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	const maxAttempts = 4
	attemptLimit := maxAttempts
	if opt.DisableRetries {
		attemptLimit = 1
	}
	var lastErr error
	for attempt := 0; attempt < attemptLimit; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 2s, 4s, 8s — or Retry-After when given.
			delay := time.Duration(1<<uint(attempt-1)) * 2 * time.Second
			if ra := headerRetryAfter(lastErr); ra > 0 {
				delay = ra
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
		}

		text, err := c.doChat(ctx, model, body)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return "", err
		}
	}
	return "", lastErr
}

func (c *Client) doChat(ctx context.Context, model string, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Transport-level failures (connection refused etc.) fail fast —
		// the per-stage fallbacks handle a dead gateway; retrying here
		// would multiply every offline/test call by the backoff.
		return "", fmt.Errorf("http do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("newapi %d: %s", resp.StatusCode, string(raw))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return "", &retryableError{cause: err, retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		return "", err
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") ||
		strings.HasPrefix(strings.TrimSpace(string(raw)), "data:") {
		return parseSSE(raw)
	}

	var result ChatResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty choices")
	}
	return messageText(result.Choices[0].Message.Content), nil
}

func messageText(content any) string {
	text, _ := content.(string)
	return text
}

// parseSSE handles OpenAI-compatible gateways that always stream chat chunks,
// even when the request does not set stream=true. It also turns a provider's
// streamed refusal into an error so callers use their normal fallback path.
func parseSSE(raw []byte) (string, error) {
	type chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	var content strings.Builder
	var providerError string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var part chunk
		if err := json.Unmarshal([]byte(payload), &part); err != nil {
			return "", fmt.Errorf("decode SSE chunk: %w", err)
		}
		if part.Error != nil && part.Error.Message != "" {
			providerError = part.Error.Message
		}
		for _, choice := range part.Choices {
			content.WriteString(choice.Delta.Content)
		}
	}
	if content.Len() > 0 {
		return content.String(), nil
	}
	if providerError != "" {
		return "", fmt.Errorf("provider stream error: %s", providerError)
	}
	return "", fmt.Errorf("empty SSE response")
}

// retryableError marks HTTP failures worth retrying (429/5xx).
type retryableError struct {
	cause      error
	retryAfter time.Duration
}

func (e *retryableError) Error() string { return e.cause.Error() }
func (e *retryableError) Unwrap() error { return e.cause }

func isRetryable(err error) bool {
	var re *retryableError
	return errors.As(err, &re)
}

func headerRetryAfter(err error) time.Duration {
	var re *retryableError
	if errors.As(err, &re) {
		return re.retryAfter
	}
	return 0
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return 0
}

// ChatJSON sends a chat completion and unmarshals the response into out.
// Sets JSONMode=true and expects the model to return valid JSON. Content
// that arrives truncated or prose-wrapped (HTTP 200 but broken JSON) is
// retried — HTTP-level retries cannot see it.
func (c *Client) ChatJSON(ctx context.Context, model string, messages []Message, out interface{}) error {
	return c.ChatJSONWithOptions(ctx, model, messages, out, ChatOptions{})
}

// ChatJSONWithOptions sends a JSON-mode request with explicit token/reasoning controls.
func (c *Client) ChatJSONWithOptions(ctx context.Context, model string, messages []Message, out interface{}, options ChatOptions) error {
	const contentAttempts = 3
	var lastErr error
	for attempt := 0; attempt < contentAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		options.JSONMode = true
		text, err := c.Chat(ctx, model, messages, options)
		if err != nil {
			return err // HTTP-level retry/backoff already applied inside Chat
		}
		if err := json.Unmarshal([]byte(text), out); err == nil {
			return nil
		} else {
			lastErr = err
		}
		// Fallback: try to extract JSON from text that might have code-fences.
		cleaned := extractJSON(text)
		if cleaned != "" {
			if err := json.Unmarshal([]byte(cleaned), out); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
		lastErr = fmt.Errorf("unmarshal LLM JSON response: %w (raw: %s)", lastErr, truncate(text, 500))
	}
	return lastErr
}

// SimpleChat is a convenience wrapper for system+user message pairs.
func (c *Client) SimpleChat(ctx context.Context, model, system, user string) (string, error) {
	return c.Chat(ctx, model, []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	})
}

// extractJSON tries to pull a JSON object/array from text that may
// have markdown code-fences or surrounding prose.
func extractJSON(s string) string {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '{' || s[i] == '[' {
			start = i
			break
		}
	}
	if start == -1 {
		return ""
	}
	end := -1
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' {
			esc = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if c == '{' || c == '[' {
			depth++
		}
		if c == '}' || c == ']' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	if end == -1 {
		return ""
	}
	return s[start : end+1]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
