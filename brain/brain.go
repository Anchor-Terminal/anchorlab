// Package brain asks the model. The model runs on the lab box itself (llama.cpp's server, or
// anything else that speaks the OpenAI chat format) and the harness only ever calls it on this
// machine: the prompts hold strangers' tool descriptions and answers, and they don't leave the box.
package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Message is one turn of the conversation.
type Message struct {
	Role    string `json:"role"` // system | user | assistant | tool
	Content string `json:"content"`
	// ToolCalls are the tools an assistant's reply asks for, in the OpenAI chat format
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID is the call a tool message answers
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall is one call a reply asks for: Arguments is the JSON object the model wrote, as text.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // function
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool is one tool offered to the model, in the OpenAI chat format.
type Tool struct {
	Type     string   `json:"type"` // function
	Function Function `json:"function"`
}

// Function is a tool's name, what it does and its arguments' JSON Schema.
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Usage is what a reply cost, as the server counted it.
type Usage struct {
	PromptTokens int `json:"prompt_tokens"`
	OutputTokens int `json:"completion_tokens"`
}

// Client calls POST <URL>/chat/completions.
type Client struct {
	URL       string // http://127.0.0.1:8080/v1
	Model     string
	MaxTokens int
	Timeout   time.Duration
	// Temperature is sent when set. Unset, the model's server uses its own setting for the model,
	// which in MODELS.md's presets is the one the model's publisher recommends.
	Temperature *float64
}

// Local reports whether raw is an address on this machine.
func Local(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("not http")
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%s isn't this machine", host)
}

// Chat sends the conversation and returns the reply's text.
func (c *Client) Chat(ctx context.Context, msgs []Message) (string, Usage, error) {
	m, u, err := c.ChatTools(ctx, msgs, nil)
	return m.Content, u, err
}

// ChatTools sends the conversation with tools the model may call, and returns the reply: its text,
// the calls it asks for, or both.
func (c *Client) ChatTools(ctx context.Context, msgs []Message, tools []Tool) (Message, Usage, error) {
	if err := Local(c.URL); err != nil {
		return Message{}, Usage{}, fmt.Errorf("the model's address: %w", err)
	}
	ask := map[string]any{"model": c.Model, "messages": msgs, "max_tokens": c.MaxTokens, "stream": false}
	if len(tools) > 0 {
		ask["tools"] = tools
	}
	if c.Temperature != nil {
		ask["temperature"] = *c.Temperature
	}
	body, _ := json.Marshal(ask)
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Message{}, Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Message{}, Usage{}, fmt.Errorf("the model answered HTTP %d: %.300s", resp.StatusCode, raw)
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return Message{}, Usage{}, errors.New("the model's answer isn't a chat completion")
	}
	m := out.Choices[0].Message
	m.Role = "assistant"
	return m, out.Usage, nil
}
