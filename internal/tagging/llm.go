package tagging

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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// ErrorKind 区分模型接口错误的处理方式。
type ErrorKind string

const (
	KindAuth       ErrorKind = "auth"        // 401/403：Key 无效或无权限 → 停止，等待修改设置
	KindModel      ErrorKind = "model"       // 404：模型或地址不存在 → 停止，等待修改设置
	KindRateLimit  ErrorKind = "rate_limit"  // 429：限流 → 整体退避
	KindQuota      ErrorKind = "quota"       // 429 且配额耗尽 → 长时间退避
	KindServer     ErrorKind = "server"      // 5xx → 整体短退避
	KindNetwork    ErrorKind = "network"     // 连接/超时 → 整体短退避
	KindBadRequest ErrorKind = "bad_request" // 400 等 → 单曲失败
	KindFormat     ErrorKind = "format"      // 不支持 response_format → 降级为 json_object
	KindFiltered   ErrorKind = "filtered"    // 内容被过滤/空输出 → 单曲失败
)

// LLMError 是一次调用的错误。Message 不含 API Key。
type LLMError struct {
	Kind       ErrorKind
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *LLMError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
	}
	return e.Message
}

// ChatClient 是 OpenAI 兼容的 Chat Completions 客户端。
type ChatClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewChatClient 创建客户端。proxyURL 为空时使用环境变量中的代理（HTTP(S)_PROXY）。
func NewChatClient(baseURL, apiKey, proxyURL string, timeout time.Duration) (*ChatClient, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	if strings.TrimSpace(proxyURL) != "" {
		u, err := url.Parse(strings.TrimSpace(proxyURL))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return nil, errors.New("代理地址无效（支持 http://、https://、socks5://）")
		}
		transport.Proxy = http.ProxyURL(u)
	}
	return &ChatClient{BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), APIKey: apiKey,
		HTTP: &http.Client{Transport: transport, Timeout: timeout}}, nil
}

// ValidateBaseURL 校验接口地址：必须是 http(s)，且不含账号密码与查询参数。
func ValidateBaseURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("接口地址必须是 http:// 或 https:// 开头的完整地址")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("接口地址不能包含账号密码或查询参数（API Key 请填在单独的输入框）")
	}
	return nil
}

// ChatRequest 是一次请求。Format：json_schema（默认）或 json_object。
type ChatRequest struct {
	Model           string
	System          string
	User            string
	Schema          json.RawMessage
	Format          string
	ReasoningEffort string
}

// ChatResult 是一次成功调用的结果。
type ChatResult struct {
	Content      string
	FinishReason string
	Usage        storage.AIUsage
	Latency      time.Duration
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Complete 调用 /chat/completions。temperature 固定为 0。
func (c *ChatClient) Complete(ctx context.Context, req ChatRequest) (ChatResult, error) {
	body := map[string]any{
		"model":       req.Model,
		"messages":    []chatMessage{{Role: "system", Content: req.System}, {Role: "user", Content: req.User}},
		"temperature": 0,
	}
	switch req.Format {
	case "json_object":
		body["response_format"] = map[string]any{"type": "json_object"}
	default:
		body["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "track_tags", "strict": true, "schema": req.Schema}}
	}
	if req.ReasoningEffort != "" {
		body["reasoning_effort"] = req.ReasoningEffort
	}
	raw, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return ChatResult{}, &LLMError{Kind: KindBadRequest, Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	start := time.Now()
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return ChatResult{}, ctx.Err()
		}
		return ChatResult{}, &LLMError{Kind: KindNetwork, Message: c.redact(networkMessage(err))}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return ChatResult{}, &LLMError{Kind: KindNetwork, Message: c.redact("读取响应失败：" + err.Error())}
	}
	if resp.StatusCode/100 != 2 {
		return ChatResult{}, c.classify(resp, data)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
				Refusal string          `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int64 `json:"prompt_tokens"`
			CompletionTokens    int64 `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ChatResult{}, &LLMError{Kind: KindServer, Status: resp.StatusCode, Message: "响应不是有效的 JSON"}
	}
	res := ChatResult{Latency: time.Since(start), Usage: storage.AIUsage{Requests: 1,
		PromptTokens: parsed.Usage.PromptTokens, CompletionTokens: parsed.Usage.CompletionTokens,
		CachedTokens: parsed.Usage.PromptTokensDetails.CachedTokens, ReasoningTokens: parsed.Usage.CompletionTokensDetails.ReasoningTokens}}
	if len(parsed.Choices) == 0 {
		return res, &LLMError{Kind: KindFiltered, Message: "模型没有返回结果（可能被内容过滤）"}
	}
	ch := parsed.Choices[0]
	res.FinishReason = ch.FinishReason
	res.Content = contentText(ch.Message.Content)
	if strings.TrimSpace(res.Content) == "" {
		msg := "模型返回了空内容"
		if ch.Message.Refusal != "" {
			msg = "模型拒绝回答：" + ch.Message.Refusal
		} else if ch.FinishReason != "" && ch.FinishReason != "stop" {
			msg += "（finish_reason=" + ch.FinishReason + "）"
		}
		return res, &LLMError{Kind: KindFiltered, Message: msg}
	}
	return res, nil
}

// contentText 兼容 content 为字符串或分段数组两种格式。
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}

func (c *ChatClient) classify(resp *http.Response, data []byte) *LLMError {
	msg := errorMessage(data)
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	msg = c.redact(msg)
	e := &LLMError{Status: resp.StatusCode, Message: msg}
	lower := strings.ToLower(msg)
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		e.Kind = KindAuth
	case resp.StatusCode == 404:
		e.Kind = KindModel
	case resp.StatusCode == 429:
		e.Kind = KindRateLimit
		if strings.Contains(lower, "quota") || strings.Contains(lower, "resource_exhausted") || strings.Contains(lower, "insufficient") || strings.Contains(lower, "billing") {
			e.Kind = KindQuota
		}
		e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))
	case resp.StatusCode >= 500:
		e.Kind = KindServer
		e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))
	case resp.StatusCode == 400 && (strings.Contains(lower, "response_format") || strings.Contains(lower, "json_schema") || strings.Contains(lower, "schema")):
		e.Kind = KindFormat
	default:
		e.Kind = KindBadRequest
	}
	return e
}

// errorMessage 兼容 OpenAI {"error":{"message"}} 与 Gemini 的数组格式。
func errorMessage(data []byte) string {
	var obj struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &obj) == nil {
		if obj.Error.Message != "" {
			return strings.TrimSpace(obj.Error.Status + " " + obj.Error.Message)
		}
		if obj.Message != "" {
			return obj.Message
		}
	}
	var arr []struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &arr) == nil && len(arr) > 0 && arr[0].Error.Message != "" {
		return strings.TrimSpace(arr[0].Error.Status + " " + arr[0].Error.Message)
	}
	s := strings.TrimSpace(string(data))
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300])
	}
	return s
}

func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func networkMessage(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "请求超时（检查网络与代理设置）"
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return "连接失败：" + ue.Err.Error()
	}
	return "连接失败：" + err.Error()
}

var userinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)

// redact 去掉错误信息中可能出现的 API Key 与 URL 中的账号密码。
func (c *ChatClient) redact(s string) string {
	if c.APIKey != "" && len(c.APIKey) >= 6 {
		s = strings.ReplaceAll(s, c.APIKey, "<api-key>")
	}
	return userinfo.ReplaceAllString(s, "$1***@")
}
