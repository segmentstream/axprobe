// Package llm is a tiny OpenRouter client (OpenAI-compatible chat completions
// with tool calling). OpenRouter is the single endpoint for every model, so the
// same harness can drive a scenario with any model — including cheap, weak ones,
// which is exactly the signal we want: a good product is drivable even by a
// simple model.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

const endpoint = "https://openrouter.ai/api/v1/chat/completions"

// Client talks to one model via OpenRouter.
type Client struct {
	APIKey string
	Model  string
	HTTP   *http.Client
}

// New reads OPENROUTER_API_KEY from the environment. The key never has to be
// passed on the command line or written into a manifest.
func New(model string) (*Client, error) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY is not set (export it to use the LLM driver)")
	}
	return &Client{
		APIKey: key,
		Model:  model,
		HTTP:   &http.Client{Timeout: 180 * time.Second},
	}, nil
}

// Message is one chat message (OpenAI-compatible).
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the called name and its raw JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a function the model may call.
type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef describes a tool's name, purpose and JSON-schema parameters.
type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
}

// Usage is token accounting (and cost, when OpenRouter reports it) for a call.
type Usage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat sends the conversation plus tool definitions and returns the model's reply
// and the call's token usage.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (Message, Usage, error) {
	body, err := json.Marshal(chatRequest{Model: c.Model, Messages: messages, Tools: tools})
	if err != nil {
		return Message{}, Usage{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Message{}, Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://github.com/segmentstream/axprobe")
	req.Header.Set("X-Title", "axprobe")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return Message{}, Usage{}, fmt.Errorf("decode response: %w", err)
	}
	if cr.Error != nil {
		return Message{}, Usage{}, fmt.Errorf("openrouter: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return Message{}, Usage{}, fmt.Errorf("openrouter: no choices returned (model %q)", c.Model)
	}
	return cr.Choices[0].Message, cr.Usage, nil
}
