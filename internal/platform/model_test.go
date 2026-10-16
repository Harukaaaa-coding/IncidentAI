package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSearchModelCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing bearer credential")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-5.2" || body["temperature"] != 0.2 || body["max_tokens"] != float64(128) {
			t.Errorf("incompatible payload %#v", body)
		}
		if len(body["messages"].([]any)) != 2 {
			t.Error("expected system and user messages")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"ok\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer server.Close()
	model, err := ParseModel([]byte("report_model:\n  provider: bai\n  model: gpt-5.2\n  api_key: test-key\n  endpoint: " + server.URL + "\nimage_model:\n  model: ignored\n"))
	if err != nil {
		t.Fatal(err)
	}
	raw, usage, err := model.Complete(context.Background(), "instructions", "evidence", 128)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"summary":"ok"}` || usage.InputTokens != 10 {
		t.Fatalf("unexpected result %s %+v", raw, usage)
	}
}
