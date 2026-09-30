package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestChatRetries429 pins the rate-limit behaviour: 429 responses are
// retried with backoff (the first live run collapsed the whole answer stage
// into fallbacks because a burst of 429s had no retry).
func TestChatRetries429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	got, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got != "ok" {
		t.Errorf("got %q", got)
	}
	if calls < 3 {
		t.Errorf("expected retries, calls=%d", calls)
	}
}

// TestChatNoRetryOn400: client errors (400) must fail without retry.
func TestChatNoRetryOn400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	_, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("400 must not retry, calls=%d", calls)
	}
}

func TestChatCanDisableRetriesOnServerError(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable"}}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "key").Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}}, ChatOptions{DisableRetries: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("disabled retries must make one request, calls=%d", calls)
	}
}

// TestChatDeadEndpointFastFail: a dead gateway must fail on the first
// attempt — retrying connection-refused multiplied every offline fallback
// call by the full backoff (the 10-minute test-timeout incident).
func TestChatDeadEndpointFastFail(t *testing.T) {
	c := New("http://127.0.0.1:1", "key")
	start := time.Now()
	_, err := c.Chat(context.Background(), "m", []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("dead endpoint took %v — must fail fast", elapsed)
	}
}

func TestChatParsesSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	got, err := New(srv.URL, "key").Chat(context.Background(), "m", nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got != "hello" {
		t.Fatalf("got %q, want hello", got)
	}
}

func TestChatUsesProviderReasoningDefaults(t *testing.T) {
	var mu sync.Mutex
	var requests []ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "test-key")
	client.ReasoningEffort = "low"
	client.MaxTokens = 4096
	if _, err := client.Chat(context.Background(), "glm-5.2", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Chat(context.Background(), "glm-5.2", nil, ChatOptions{MaxTokens: 8, DisableRetries: true}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	if requests[0].ReasoningEffort != "low" || requests[0].MaxTokens != 4096 {
		t.Errorf("provider defaults = effort %q, max_tokens %d", requests[0].ReasoningEffort, requests[0].MaxTokens)
	}
	if requests[1].ReasoningEffort != "low" || requests[1].MaxTokens != 8 {
		t.Errorf("per-request override = effort %q, max_tokens %d", requests[1].ReasoningEffort, requests[1].MaxTokens)
	}
}
