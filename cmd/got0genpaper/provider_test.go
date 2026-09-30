package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderConnectionUsesOpenAICompatibleEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization header missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"content": "OK"}}},
		})
	}))
	defer server.Close()

	a := &API{}
	result := a.TestProviderConnection(ProviderConnectionRequest{BaseURL: server.URL, APIKey: "test-key", Model: "test-model"})
	if !result.OK || result.Message != "连接成功" {
		t.Fatalf("connection result = %+v", result)
	}
}

func TestProviderVisionConnectionSendsImageInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		payload := string(body)
		if !strings.Contains(payload, `"type":"image_url"`) || !strings.Contains(payload, "data:image/png;base64,") {
			t.Fatalf("multimodal image part missing from request: %s", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer server.Close()

	result := (&API{}).TestProviderVisionConnection(ProviderConnectionRequest{BaseURL: server.URL, APIKey: "test-key", Model: "vision-model"})
	if !result.OK || result.Message != "多模态连接成功" {
		t.Fatalf("vision connection result = %+v", result)
	}
}

func TestProviderConnectionMapsAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()

	result := (&API{}).TestProviderConnection(ProviderConnectionRequest{BaseURL: server.URL, APIKey: "bad", Model: "test-model"})
	if result.OK || result.Message != "鉴权失败，请检查 API Key" {
		t.Fatalf("auth result = %+v", result)
	}
}

func TestProviderConnectionMapsUpstreamModelPermissionFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"model is not available in the current token plan"}}`))
	}))
	defer server.Close()

	result := (&API{}).TestProviderConnection(ProviderConnectionRequest{BaseURL: server.URL, APIKey: "test-key", Model: "test-model"})
	if result.OK || result.Message != "上游账号当前套餐无权调用该模型，请在上游确认模型权限" {
		t.Fatalf("connection result = %+v", result)
	}
}

func TestProviderConnectionMapsGatewayModelPriceFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"model_price_error","message":"model price not configured"}}`))
	}))
	defer server.Close()

	result := (&API{}).TestProviderConnection(ProviderConnectionRequest{BaseURL: server.URL, APIKey: "test-key", Model: "test-model"})
	if result.OK || result.Message != "本地网关未配置该模型的计费倍率或价格" {
		t.Fatalf("connection result = %+v", result)
	}
}
