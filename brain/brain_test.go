package brain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Unset, the temperature is left to the model's server; set, it's sent.
func TestTemperature(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL + "/v1", Model: "gpt-oss-120b", MaxTokens: 100, Timeout: 5 * time.Second}
	if _, _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if _, sent := got["temperature"]; sent || got["model"] != "gpt-oss-120b" || got["max_tokens"] != float64(100) {
		t.Errorf("unset: %v", got)
	}
	temp := 0.2
	c.Temperature = &temp
	c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if got["temperature"] != 0.2 {
		t.Errorf("set: %v", got)
	}
}
