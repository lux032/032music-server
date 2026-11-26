package tagging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChatClientRetriesWithoutTemperatureOnUnsupported400(t *testing.T) {
	var calls int
	var sawTemp []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_, hasTemp := body["temperature"]
		sawTemp = append(sawTemp, hasTemp)
		if hasTemp {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported value: 'temperature' does not support 0 with this model. Only the default (1) value is supported."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": `{"ok":true}`}, "finish_reason": "stop"},
			},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4},
		})
	}))
	defer srv.Close()

	c, err := NewChatClient(srv.URL, "sk-test", "", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	req := ChatRequest{Model: "gpt-6-luna", System: "Reply JSON", User: "ping", Format: "json_object"}
	res, err := c.Complete(context.Background(), req)
	if err != nil || res.Content != `{"ok":true}` {
		t.Fatalf("first Complete = (%+v, %v)", res, err)
	}
	if calls != 2 || len(sawTemp) != 2 || !sawTemp[0] || sawTemp[1] {
		t.Fatalf("expected retry without temperature, calls=%d sawTemp=%v", calls, sawTemp)
	}
	// Second call with the same model must immediately omit temperature without incurring another 400.
	res, err = c.Complete(context.Background(), req)
	if err != nil || calls != 3 || sawTemp[2] {
		t.Fatalf("cached omitTemp failed: calls=%d sawTemp=%v err=%v", calls, sawTemp, err)
	}
}
