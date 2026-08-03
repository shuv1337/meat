package meat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultAnthropicModel is used when AnthropicModel.Model is empty or the
// provider-specific constructor receives no model selection.
const DefaultAnthropicModel = "claude-sonnet-5"

// AnthropicModel is a built-in Model backed by the Anthropic Messages API. It
// uses only the standard library so meat.dev has no third-party dependencies.
type AnthropicModel struct {
	APIKey  string       // API key, OAuth access token (sk-ant-oat…), or "implicit" for the exe.dev gateway
	Model   string       // defaults to DefaultAnthropicModel
	BaseURL string       // bare origin/prefix; defaults to https://api.anthropic.com. "/v1/messages" is appended.
	OAuth   bool         // when true, use Claude Pro/Max subscription wire contract (Bearer + Claude Code identity)
	HTTPC   *http.Client // defaults to a client with a 2m timeout
}

// maxOutputTokens is the per-turn output cap sent to the API. Large enough for
// an edit plan over a sizable diff; a response that still stops at max_tokens
// is reported as an error rather than silently truncated.
const maxOutputTokens = 16384

// implicitGatewayKey is the placeholder API key sent to the exe.dev LLM
// gateway. The gateway injects the real managed credential at the network edge,
// so no provider key needs to live on the VM.
const implicitGatewayKey = "implicit"

// NewAnthropicFromEnv builds an AnthropicModel, preferring the exe.dev managed
// LLM gateway when available so meat works on an exe.dev VM with no API key.
//
// Resolution order:
//  1. If ANTHROPIC_API_KEY (or ANTHROPIC_BASE_URL) is set, use it directly.
//  2. Else if a Claude Pro/Max OAuth credential is stored (meat login anthropic), use it.
//  3. Otherwise, on an exe.dev VM with an attached "llm" integration, route
//     through the gateway at https://<llm-host>/anthropic with managed creds.
//  4. Otherwise, error.
//
// model falls back to $MEAT_MODEL, then DefaultAnthropicModel.
func NewAnthropicFromEnv(ctx context.Context, model string) (*AnthropicModel, error) {
	model = resolveModel(model, DefaultAnthropicModel)

	key := os.Getenv("ANTHROPIC_API_KEY")
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if key != "" || baseURL != "" {
		m := &AnthropicModel{APIKey: key, Model: model, BaseURL: baseURL}
		if isAnthropicOAuthToken(key) {
			m.OAuth = true
		}
		return m, nil
	}

	if cred, ok, err := LoadOAuthCredential(OAuthProviderAnthropic); err != nil {
		return nil, err
	} else if ok {
		fresh, err := ResolveAnthropicOAuth(false)
		if err != nil {
			if !oauthNeedsRefresh(cred, time.Now()) {
				fresh = cred
			} else {
				return nil, err
			}
		}
		return &AnthropicModel{
			APIKey: fresh.Access,
			Model:  model,
			OAuth:  true,
		}, nil
	}

	// No explicit credentials: try the exe.dev managed gateway.
	if base := discoverExeGatewayBase(ctx, nil); base != "" {
		return &AnthropicModel{
			APIKey:  implicitGatewayKey,
			Model:   model,
			BaseURL: base + "/anthropic",
		}, nil
	}

	return nil, fmt.Errorf("no LLM credentials: set ANTHROPIC_API_KEY, run `meat login anthropic`, or use an exe.dev VM with an attached 'llm' integration")
}

// --- wire types ---

type antReq struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system"`
	Messages  []antMessage `json:"messages"`
	Tools     []antTool    `json:"tools,omitempty"`
}

type antMessage struct {
	Role    string     `json:"role"`
	Content []antBlock `json:"content"`
}

type antBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type antResp struct {
	Content    []antBlock `json:"content"`
	StopReason string     `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Generate implements Model.
func (m *AnthropicModel) Generate(ctx context.Context, system string, messages []Message, tools []Tool) (*Response, error) {
	if m.APIKey == "" {
		return nil, fmt.Errorf("meat: AnthropicModel.APIKey is empty")
	}

	oauth := m.OAuth || isAnthropicOAuthToken(m.APIKey)
	apiKey := m.APIKey
	if oauth && m.APIKey != implicitGatewayKey {
		if cred, err := ResolveAnthropicOAuth(false); err == nil {
			apiKey = cred.Access
			m.APIKey = cred.Access
		}
	}

	systemPayload := any(system)
	if oauth {
		// Subscription inference requires the Claude Code identity block first.
		systemPayload = []map[string]string{
			{"type": "text", "text": claudeCodeSystemIdentity},
			{"type": "text", "text": system},
		}
	}

	reqBody := map[string]any{
		"model":      cmpOr(m.Model, DefaultAnthropicModel),
		"max_tokens": maxOutputTokens,
		"system":     systemPayload,
		"messages":   toAntMessages(messages),
	}
	if antTools := toAntTools(tools); len(antTools) > 0 {
		reqBody["tools"] = antTools
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	base := strings.TrimRight(cmpOr(m.BaseURL, "https://api.anthropic.com"), "/")
	endpoint := base + "/v1/messages"
	if oauth {
		endpoint += "?beta=true"
	}
	client := m.HTTPC
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	raw, err := postAnthropicWithAuth(ctx, client, endpoint, apiKey, oauth, body)
	if err != nil && oauth && isUnauthorizedErr(err) {
		cred, refreshErr := ResolveAnthropicOAuth(true)
		if refreshErr == nil {
			m.APIKey = cred.Access
			raw, err = postAnthropicWithAuth(ctx, client, endpoint, cred.Access, true, body)
		}
	}
	if err != nil {
		return nil, err
	}

	var resp antResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("anthropic error: %s: %s", resp.Error.Type, resp.Error.Message)
	}
	// A max_tokens stop means the reply was cut off mid-thought — likely inside
	// a tool_use JSON block. Using it would silently produce (and cache) a
	// truncated abridgement, so fail loudly instead.
	if resp.StopReason == "max_tokens" {
		return nil, fmt.Errorf("anthropic response truncated at max_tokens (%d); the diff may be too large to abridge in one pass", maxOutputTokens)
	}

	out := &Response{
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	}
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			out.Content = append(out.Content, Block{Type: "text", Text: b.Text})
		case "tool_use":
			out.Content = append(out.Content, Block{Type: "tool_use", ID: b.ID, ToolName: b.Name, ToolInput: b.Input})
		}
	}
	return out, nil
}

// maxAttempts bounds retries of a single API call (1 initial + retries).
const maxAttempts = 4

// retryBaseDelay is the first backoff step (doubled each retry). A var so
// tests can shrink it.
var retryBaseDelay = time.Second

// postWithRetry POSTs body to the Anthropic messages endpoint, retrying
// transient failures (429 rate limits, 5xx including Anthropic's 529
// overloaded_error, and transport errors) with exponential backoff. It returns
// the raw response body on the first 200. Non-retryable statuses (4xx other
// than 408/429) fail immediately — retrying a bad request can't help.
func postWithRetry(ctx context.Context, client *http.Client, url, apiKey string, body []byte) ([]byte, error) {
	return postAnthropicWithAuth(ctx, client, url, apiKey, false, body)
}

func postAnthropicWithAuth(ctx context.Context, client *http.Client, url, apiKey string, oauth bool, body []byte) ([]byte, error) {
	return postJSONWithRetry(ctx, client, url, body, "anthropic", func(req *http.Request) {
		req.Header.Set("anthropic-version", "2023-06-01")
		if oauth {
			req.Header.Set("authorization", "Bearer "+apiKey)
			req.Header.Del("x-api-key")
			req.Header.Set("anthropic-beta", anthropicOAuthBetas)
			req.Header.Set("user-agent", "claude-cli/"+claudeCodeVersion+" (external, cli)")
			req.Header.Set("x-app", "cli")
		} else {
			req.Header.Set("x-api-key", apiKey)
		}
	})
}

func isUnauthorizedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, " API 401") || strings.Contains(msg, " API 403")
}

// postJSONWithRetry POSTs one JSON request, retrying transient failures (429
// rate limits, 5xx, and transport errors) with exponential backoff. Successful
// streaming responses are read to completion and returned as raw bytes.
func postJSONWithRetry(ctx context.Context, client *http.Client, url string, body []byte, provider string, setHeaders func(*http.Request)) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// 1s, 2s, 4s — modest; the Abridge wall-clock budget is the real cap.
			delay := retryBaseDelay << (attempt - 1)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("content-type", "application/json")
		if setHeaders != nil {
			setHeaders(req)
		}

		httpResp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err // transport error: retry
			continue
		}
		raw, err := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if httpResp.StatusCode == http.StatusOK {
			return raw, nil
		}
		lastErr = fmt.Errorf("%s API %d: %s", provider, httpResp.StatusCode, strings.TrimSpace(string(raw)))
		if !retryableStatus(httpResp.StatusCode) {
			return nil, lastErr
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}

// retryableStatus reports whether an HTTP status is worth retrying: request
// timeout, rate limiting, and server-side failures (Anthropic uses 529 for
// overloaded_error).
func retryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

func toAntTools(tools []Tool) []antTool {
	out := make([]antTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, antTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

func toAntMessages(messages []Message) []antMessage {
	out := make([]antMessage, 0, len(messages))
	for _, m := range messages {
		am := antMessage{Role: string(m.Role)}
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				am.Content = append(am.Content, antBlock{Type: "text", Text: b.Text})
			case "tool_use":
				am.Content = append(am.Content, antBlock{Type: "tool_use", ID: b.ID, Name: b.ToolName, Input: b.ToolInput})
			case "tool_result":
				am.Content = append(am.Content, antBlock{Type: "tool_result", ToolUseID: b.ToolUseID, Content: b.ToolResult, IsError: b.ToolError})
			}
		}
		out = append(out, am)
	}
	return out
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
