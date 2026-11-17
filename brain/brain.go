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
	Role    string `json:"role"` // system | user | assistant
	Content string `json:"content"`
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
	if err := Local(c.URL); err != nil {
		return "", Usage{}, fmt.Errorf("the model's address: %w", err)
	}
	body, _ := json.Marshal(map[string]any{"model": c.Model, "messages": msgs, "temperature": 0.2, "max_tokens": c.MaxTokens, "stream": false})
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.URL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, fmt.Errorf("the model answered HTTP %d: %.300s", resp.StatusCode, raw)
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", Usage{}, errors.New("the model's answer isn't a chat completion")
	}
	return out.Choices[0].Message.Content, out.Usage, nil
}
