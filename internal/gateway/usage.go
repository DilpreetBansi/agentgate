package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

// Usage is what the gateway learns about a completed call.
type Usage struct {
	Model     string
	InputTok  int64
	OutputTok int64
}

// requestModel reads the "model" field from a request body, if any.
func requestModel(body []byte) string {
	var r struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &r)
	return r.Model
}

// usageEnvelope covers both providers' response shapes:
// Anthropic: {"model":..., "usage":{"input_tokens","output_tokens","cache_*"}}
// OpenAI:    {"model":..., "usage":{"prompt_tokens","completion_tokens"}}
type usageEnvelope struct {
	Type    string `json:"type"`
	Model   string `json:"model"`
	Message *struct {
		Model string     `json:"model"`
		Usage *usageBody `json:"usage"`
	} `json:"message"`
	Usage *usageBody `json:"usage"`
}

type usageBody struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	PromptTokens             int64 `json:"prompt_tokens"`
	CompletionTokens         int64 `json:"completion_tokens"`
}

func (u *usageBody) in() int64 {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens + u.PromptTokens
}
func (u *usageBody) out() int64 { return u.OutputTokens + u.CompletionTokens }

// parseUsage extracts token usage from a buffered response body. It handles
// plain JSON and server-sent event streams from both providers.
func parseUsage(contentType string, body []byte) Usage {
	if strings.Contains(contentType, "text/event-stream") || bytes.HasPrefix(bytes.TrimSpace(body), []byte("event:")) || bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:")) {
		return parseSSE(body)
	}
	var env usageEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Usage{}
	}
	u := Usage{Model: env.Model}
	if env.Usage != nil {
		u.InputTok, u.OutputTok = env.Usage.in(), env.Usage.out()
	}
	return u
}

// parseSSE walks a stream. Anthropic sends input tokens in message_start and
// the running output count in message_delta; OpenAI sends one usage object in
// the final chunk when stream_options.include_usage is set.
func parseSSE(body []byte) Usage {
	var u Usage
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var env usageEnvelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch {
		case env.Type == "message_start" && env.Message != nil:
			if env.Message.Model != "" {
				u.Model = env.Message.Model
			}
			if env.Message.Usage != nil {
				u.InputTok = env.Message.Usage.in()
				u.OutputTok = env.Message.Usage.out()
			}
		case env.Type == "message_delta" && env.Usage != nil:
			// Anthropic reports the cumulative output count here.
			if o := env.Usage.out(); o > u.OutputTok {
				u.OutputTok = o
			}
			if i := env.Usage.in(); i > u.InputTok {
				u.InputTok = i
			}
		case env.Usage != nil:
			if env.Model != "" {
				u.Model = env.Model
			}
			u.InputTok, u.OutputTok = env.Usage.in(), env.Usage.out()
		default:
			if env.Model != "" && u.Model == "" {
				u.Model = env.Model
			}
		}
	}
	return u
}
