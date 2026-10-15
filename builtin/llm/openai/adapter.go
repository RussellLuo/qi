package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/RussellLuo/qi/kernel/core/llm"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

const maxResponseBytes = 4 << 20

var errConsumerStopped = errors.New("stream consumer stopped")

type Config struct {
	Provider      string
	Endpoint      string
	APIKey        string
	Headers       map[string]string
	HTTPClient    *http.Client
	ContextWindow int
	Parameters    json.RawMessage
	// MaxTokensParameter selects the backend's token limit field. The default
	// is max_completion_tokens; older compatible services can use max_tokens.
	MaxTokensParameter string
	// ReasoningHistory is omit (default) or reasoning_content. Reasoning is
	// never merged into the assistant's visible content.
	ReasoningHistory string
	// Timeout bounds each streamed HTTP request, including body reads.
	// Zero uses two minutes. An injected HTTPClient remains caller-owned.
	Timeout time.Duration
}

type Adapter struct {
	provider           string
	endpoint           string
	apiKey             string
	headers            map[string]string
	client             *http.Client
	contextWindow      int
	parameters         json.RawMessage
	maxTokensParameter string
	reasoningHistory   string
	timeout            time.Duration
}

func New(config Config) (*Adapter, error) {
	if config.Provider == "" {
		config.Provider = "openai"
	}
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("OpenAI-compatible endpoint must be an absolute HTTP(S) URL")
	}
	if config.ContextWindow < 0 {
		return nil, errors.New("OpenAI-compatible context window cannot be negative")
	}
	if config.MaxTokensParameter == "" {
		config.MaxTokensParameter = "max_completion_tokens"
	}
	if config.MaxTokensParameter != "max_completion_tokens" && config.MaxTokensParameter != "max_tokens" {
		return nil, errors.New("OpenAI-compatible max_tokens_parameter must be max_completion_tokens or max_tokens")
	}
	if config.ReasoningHistory == "" {
		config.ReasoningHistory = "omit"
	}
	if config.ReasoningHistory != "omit" && config.ReasoningHistory != "reasoning_content" {
		return nil, errors.New("OpenAI-compatible reasoning_history must be omit or reasoning_content")
	}
	if config.Timeout < 0 {
		return nil, errors.New("OpenAI-compatible timeout cannot be negative")
	}
	if config.Timeout == 0 {
		config.Timeout = 2 * time.Minute
	}
	if _, err := decodeParameters(config.Parameters); err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	headers := make(map[string]string, len(config.Headers))
	for key, value := range config.Headers {
		if strings.EqualFold(key, "Authorization") {
			return nil, errors.New("use APIKey instead of an Authorization header")
		}
		headers[key] = value
	}
	return &Adapter{
		provider: config.Provider, endpoint: config.Endpoint, apiKey: config.APIKey,
		headers: headers, client: client, contextWindow: config.ContextWindow, parameters: append(json.RawMessage(nil), config.Parameters...),
		maxTokensParameter: config.MaxTokensParameter, reasoningHistory: config.ReasoningHistory, timeout: config.Timeout,
	}, nil
}

func (a *Adapter) ResolveModel(ctx context.Context, ref llm.ModelRef) (llm.ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return llm.ModelInfo{}, err
	}
	if a == nil {
		return llm.ModelInfo{}, errors.New("OpenAI-compatible adapter is nil")
	}
	if ref.Provider != a.provider || ref.Model == "" {
		return llm.ModelInfo{}, fmt.Errorf("unsupported OpenAI-compatible model %s/%s", ref.Provider, ref.Model)
	}
	return llm.ModelInfo{
		Ref: ref, ContextWindow: a.contextWindow, Capabilities: []string{"stream", "tools"},
	}, nil
}

func (a *Adapter) Stream(ctx context.Context, request llm.Request) iter.Seq2[protocol.StreamChunk, error] {
	request = request.Clone()
	return func(yield func(protocol.StreamChunk, error) bool) {
		if a == nil {
			yield(protocol.StreamChunk{}, errors.New("OpenAI-compatible adapter is nil"))
			return
		}
		ctx, cancel := context.WithTimeout(ctx, a.timeout)
		defer cancel()
		payload, err := encodeRequest(request, Config{Parameters: a.parameters, MaxTokensParameter: a.maxTokensParameter, ReasoningHistory: a.reasoningHistory})
		if err != nil {
			yield(protocol.StreamChunk{}, err)
			return
		}
		httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(payload))
		if err != nil {
			yield(protocol.StreamChunk{}, fmt.Errorf("create OpenAI-compatible request: %w", err))
			return
		}
		httpRequest.Header.Set("Accept", "text/event-stream")
		httpRequest.Header.Set("Content-Type", "application/json")
		if a.apiKey != "" {
			httpRequest.Header.Set("Authorization", "Bearer "+a.apiKey)
		}
		for key, value := range a.headers {
			httpRequest.Header.Set(key, value)
		}
		response, err := a.client.Do(httpRequest)
		if err != nil {
			if ctx.Err() != nil {
				yield(protocol.StreamChunk{}, ctx.Err())
				return
			}
			yield(protocol.StreamChunk{}, fmt.Errorf("OpenAI-compatible request: %w", err))
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			yield(protocol.StreamChunk{}, redactCredential(decodeProviderError(response), a.apiKey))
			return
		}
		if err := decodeSSE(ctx, response.Body, yield); err != nil {
			if errors.Is(err, errConsumerStopped) {
				return
			}
			yield(protocol.StreamChunk{}, redactCredential(err, a.apiKey))
		}
	}
}

type ProviderError struct {
	StatusCode int
	Type       string
	Code       string
	Message    string
}

func (e *ProviderError) Error() string {
	if e == nil {
		return "OpenAI-compatible provider error"
	}
	detail := e.Message
	if detail == "" {
		detail = http.StatusText(e.StatusCode)
	}
	if e.Code != "" {
		return fmt.Sprintf("OpenAI-compatible provider error (%d, %s): %s", e.StatusCode, e.Code, detail)
	}
	return fmt.Sprintf("OpenAI-compatible provider error (%d): %s", e.StatusCode, detail)
}

func decodeProviderError(response *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read OpenAI-compatible error response: %w", err)
	}
	if len(body) > maxResponseBytes {
		body = body[:maxResponseBytes]
	}
	var envelope providerErrorEnvelope
	_ = json.Unmarshal(body, &envelope)
	message := envelope.Error.Message
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	return &ProviderError{
		StatusCode: response.StatusCode, Type: envelope.Error.Type,
		Code: stringifyCode(envelope.Error.Code), Message: message,
	}
}

type providerErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

func stringifyCode(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func redactCredential(err error, credential string) error {
	if err == nil || credential == "" || !strings.Contains(err.Error(), credential) {
		return err
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		cloned := *providerErr
		cloned.Type = strings.ReplaceAll(cloned.Type, credential, "[REDACTED]")
		cloned.Code = strings.ReplaceAll(cloned.Code, credential, "[REDACTED]")
		cloned.Message = strings.ReplaceAll(cloned.Message, credential, "[REDACTED]")
		return &cloned
	}
	return errors.New(strings.ReplaceAll(err.Error(), credential, "[REDACTED]"))
}

func decodeParameters(private json.RawMessage) (map[string]any, error) {
	parameters := make(map[string]any)
	if len(private) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(private))
		decoder.UseNumber()
		if err := decoder.Decode(&parameters); err != nil || parameters == nil {
			return nil, errors.New("OpenAI-compatible parameters must be a JSON object")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, errors.New("OpenAI-compatible parameters must contain exactly one JSON object")
		}
	}
	for _, reserved := range []string{"model", "messages", "tools", "stream", "stream_options", "temperature", "max_tokens", "max_completion_tokens", "stop", "reasoning_effort"} {
		if _, exists := parameters[reserved]; exists {
			return nil, fmt.Errorf("OpenAI-compatible parameter %q is owned by the adapter", reserved)
		}
	}
	return parameters, nil
}

func encodeRequest(request llm.Request, config Config) ([]byte, error) {
	parameters, err := decodeParameters(config.Parameters)
	if err != nil {
		return nil, err
	}
	if request.Config.ReasoningEffort != nil {
		parameters["reasoning_effort"] = *request.Config.ReasoningEffort
	}
	if request.Config.Temperature != nil {
		parameters["temperature"] = *request.Config.Temperature
	}
	if request.Config.MaxTokens != nil {
		field := config.MaxTokensParameter
		if field == "" {
			field = "max_completion_tokens"
		}
		parameters[field] = *request.Config.MaxTokens
	}
	if request.Config.Stop != nil {
		parameters["stop"] = request.Config.Stop
	}
	messages, err := encodeMessages(request, config.ReasoningHistory)
	if err != nil {
		return nil, err
	}
	parameters["model"] = request.Config.Model
	parameters["messages"] = messages
	parameters["stream"] = true
	parameters["stream_options"] = map[string]any{"include_usage": true}
	if len(request.Tools) != 0 {
		tools := make([]map[string]any, 0, len(request.Tools))
		for _, schema := range request.Tools {
			if schema.Name == "" || len(schema.Parameters) == 0 || !json.Valid(schema.Parameters) {
				return nil, fmt.Errorf("invalid Tool schema %q", schema.Name)
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": schema.Name, "description": schema.Description,
					"parameters": json.RawMessage(schema.Parameters),
				},
			})
		}
		parameters["tools"] = tools
	}
	return json.Marshal(parameters)
}

func encodeMessages(request llm.Request, reasoningHistory string) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(request.Messages)+1)
	for _, message := range request.Messages {
		if err := message.Validate(); err != nil {
			return nil, fmt.Errorf("encode OpenAI-compatible message: %w", err)
		}

		wire := map[string]any{"role": string(message.Role)}
		if message.Role == protocol.RoleSystem && len(message.Content) == 0 {
			continue
		}
		if message.Role == protocol.RoleTool {
			wire["tool_call_id"] = message.ToolCallID
		}
		var content strings.Builder
		var reasoning strings.Builder
		var calls []map[string]any
		for _, block := range message.Content {
			switch value := block.(type) {
			case protocol.TextBlock:
				content.WriteString(value.Text)
			case protocol.ToolAdditionBlock:
				content.WriteString("Tool available: " + value.ToolName + "\n")
			case protocol.ToolRemovalBlock:
				content.WriteString("Tool removed: " + value.ToolName + "\n")
			case protocol.ReasoningBlock:
				if reasoningHistory == "reasoning_content" {
					reasoning.WriteString(value.Text)
				}
			case protocol.ToolCallBlock:
				calls = append(calls, map[string]any{
					"id": value.ToolCall.ID, "type": "function",
					"function": map[string]any{"name": value.ToolCall.Name, "arguments": string(value.ToolCall.Arguments)},
				})
			default:
				return nil, fmt.Errorf("unsupported OpenAI-compatible message block %T", block)
			}
		}
		if content.Len() != 0 || len(calls) == 0 {
			wire["content"] = content.String()
		} else if len(calls) != 0 {
			wire["content"] = nil
		}
		if len(calls) != 0 {
			if message.Role != protocol.RoleAssistant {
				return nil, errors.New("only assistant messages may contain Tool Calls")
			}
			wire["tool_calls"] = calls
		}
		if reasoning.Len() != 0 {
			wire["reasoning_content"] = reasoning.String()
		}
		result = append(result, wire)
	}
	return result, nil
}

type streamEnvelope struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Refusal          string `json:"refusal"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     uint64  `json:"prompt_tokens"`
		CompletionTokens uint64  `json:"completion_tokens"`
		TotalTokens      *uint64 `json:"total_tokens"`
		PromptDetails    *struct {
			CachedTokens     *uint64 `json:"cached_tokens"`
			CacheWriteTokens *uint64 `json:"cache_write_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails *struct {
			ReasoningTokens *uint64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		CacheHitTokens  *uint64 `json:"prompt_cache_hit_tokens"`
		CacheMissTokens *uint64 `json:"prompt_cache_miss_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

type toolBuffer struct {
	index     int64
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
}
type streamState struct {
	nextIndex      int64
	textIndex      *int64
	reasoningIndex *int64
	text           strings.Builder
	reasoning      strings.Builder
	tools          map[int]*toolBuffer
	toolOrder      []int
	finish         string
	usage          protocol.Usage
	hasUsage       bool
}

func (s *streamState) allocate() int64 { index := s.nextIndex; s.nextIndex++; return index }

func decodeSSE(ctx context.Context, body io.Reader, yield func(protocol.StreamChunk, error) bool) error {
	state := streamState{tools: make(map[int]*toolBuffer)}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxResponseBytes)
	var data []string
	done := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if len(data) == 0 {
				continue
			}
			payload := strings.Join(data, "\n")
			data = data[:0]
			if payload == "[DONE]" {
				done = true
				break
			}
			if err := state.accept([]byte(payload), yield); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			field, value = line, ""
		}
		if field == "data" {
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("read OpenAI-compatible SSE: %w", err)
	}
	if !done && len(data) != 0 && strings.Join(data, "\n") == "[DONE]" {
		done = true
	}
	if !done {
		return errors.New("OpenAI-compatible SSE ended without [DONE]")
	}
	return state.finishStream(yield)
}

func (s *streamState) accept(data []byte, yield func(protocol.StreamChunk, error) bool) error {
	var event streamEnvelope
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("decode OpenAI-compatible SSE data: %w", err)
	}
	if event.Error != nil {
		return &ProviderError{StatusCode: http.StatusOK, Type: event.Error.Type, Code: stringifyCode(event.Error.Code), Message: event.Error.Message}
	}
	if event.Usage != nil {
		u := event.Usage
		s.usage = protocol.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
		if u.PromptDetails != nil {
			s.usage.CacheReadTokens = u.PromptDetails.CachedTokens
			s.usage.CacheWriteTokens = u.PromptDetails.CacheWriteTokens
		}
		if u.CacheHitTokens != nil {
			s.usage.CacheReadTokens = u.CacheHitTokens
		}
		if s.usage.CacheReadTokens != nil && u.CacheMissTokens == nil {
			if *s.usage.CacheReadTokens > u.PromptTokens {
				return errors.New("cached tokens exceed prompt tokens")
			}
			s.usage.InputTokens = u.PromptTokens - *s.usage.CacheReadTokens
		}
		if u.CacheMissTokens != nil {
			s.usage.InputTokens = *u.CacheMissTokens
		}
		if u.CompletionDetails != nil {
			s.usage.ReasoningTokens = u.CompletionDetails.ReasoningTokens
		}
		s.hasUsage = true
	}
	for _, choice := range event.Choices {
		if choice.Index != 0 {
			return fmt.Errorf("OpenAI-compatible stream returned unsupported choice index %d", choice.Index)
		}
		if choice.Delta.Refusal != "" {
			return &ProviderError{StatusCode: http.StatusOK, Type: "refusal", Code: "REFUSAL", Message: choice.Delta.Refusal}
		}
		if choice.Delta.Content != "" {
			if s.textIndex == nil {
				index := s.allocate()
				s.textIndex = &index
				if !yield(protocol.StreamChunk{Type: protocol.ChunkBlockStart, Index: index, BlockType: protocol.BlockText}, nil) {
					return errConsumerStopped
				}
			}
			s.text.WriteString(choice.Delta.Content)
			if !yield(protocol.StreamChunk{Type: protocol.ChunkTextDelta, Index: *s.textIndex, Text: choice.Delta.Content}, nil) {
				return errConsumerStopped
			}
		}
		if choice.Delta.ReasoningContent != "" {
			if s.reasoningIndex == nil {
				index := s.allocate()
				s.reasoningIndex = &index
				if !yield(protocol.StreamChunk{Type: protocol.ChunkBlockStart, Index: index, BlockType: protocol.BlockReasoning}, nil) {
					return errConsumerStopped
				}
			}
			s.reasoning.WriteString(choice.Delta.ReasoningContent)
			if !yield(protocol.StreamChunk{Type: protocol.ChunkReasoningDelta, Index: *s.reasoningIndex, Text: choice.Delta.ReasoningContent}, nil) {
				return errConsumerStopped
			}
		}
		for _, delta := range choice.Delta.ToolCalls {
			if delta.Index < 0 {
				return errors.New("negative OpenAI-compatible tool index")
			}
			buffer := s.tools[delta.Index]
			if buffer == nil {
				buffer = &toolBuffer{index: s.allocate()}
				s.tools[delta.Index] = buffer
				s.toolOrder = append(s.toolOrder, delta.Index)
				if !yield(protocol.StreamChunk{Type: protocol.ChunkBlockStart, Index: buffer.index, BlockType: protocol.BlockToolCall}, nil) {
					return errConsumerStopped
				}
			}
			buffer.id.WriteString(delta.ID)
			buffer.name.WriteString(delta.Function.Name)
			buffer.arguments.WriteString(delta.Function.Arguments)
			chunk := protocol.StreamChunk{Type: protocol.ChunkToolCallDelta, Index: buffer.index, ID: protocol.ToolCallID(buffer.id.String()), ArgumentsDelta: delta.Function.Arguments}
			if delta.Function.Name != "" {
				name := buffer.name.String()
				chunk.Name = &name
			}
			if !yield(chunk, nil) {
				return errConsumerStopped
			}
		}
		if choice.FinishReason != "" {
			if s.finish != "" && s.finish != choice.FinishReason {
				return errors.New("OpenAI-compatible stream changed finish reason")
			}
			s.finish = choice.FinishReason
		}
	}
	return nil
}

func (s *streamState) finishStream(yield func(protocol.StreamChunk, error) bool) error {
	if s.finish == "" {
		return errors.New("OpenAI-compatible stream has no finish reason")
	}
	reason, err := mapFinish(s.finish)
	if err != nil {
		return err
	}
	if s.textIndex != nil && !yield(protocol.StreamChunk{Type: protocol.ChunkBlockEnd, Index: *s.textIndex, Block: protocol.TextBlock{Text: s.text.String()}}, nil) {
		return errConsumerStopped
	}
	if s.reasoningIndex != nil && !yield(protocol.StreamChunk{Type: protocol.ChunkBlockEnd, Index: *s.reasoningIndex, Block: protocol.ReasoningBlock{Text: s.reasoning.String()}}, nil) {
		return errConsumerStopped
	}
	for _, index := range s.toolOrder {
		buffer := s.tools[index]
		call := protocol.ToolCall{ID: protocol.ToolCallID(buffer.id.String()), Name: buffer.name.String(), Arguments: buffer.arguments.String()}
		// A truncated proposal remains in the raw deltas but cannot become a closed executable block.
		if reason.Kind == protocol.FinishMaxTokens {
			continue
		}
		if err := call.Validate(); err != nil {
			return fmt.Errorf("invalid OpenAI-compatible Tool Call %d: %w", index, err)
		}
		if !yield(protocol.StreamChunk{Type: protocol.ChunkBlockEnd, Index: buffer.index, Block: protocol.ToolCallBlock{ToolCall: call}}, nil) {
			return errConsumerStopped
		}
	}
	if s.hasUsage {
		usage := s.usage
		if !yield(protocol.StreamChunk{Type: protocol.ChunkUsage, Usage: &usage}, nil) {
			return errConsumerStopped
		}
	}
	yield(protocol.StreamChunk{Type: protocol.ChunkFinish, Reason: reason}, nil)
	return nil
}
func mapFinish(reason string) (protocol.FinishReason, error) {
	var kind protocol.FinishKind
	switch reason {
	case "stop":
		kind = protocol.FinishStop
	case "tool_calls", "function_call":
		kind = protocol.FinishToolCalls
	case "length":
		kind = protocol.FinishMaxTokens
	case "content_filter":
		return protocol.FinishReason{}, &ProviderError{StatusCode: http.StatusOK, Type: "content_filter", Code: "CONTENT_FILTER", Message: "provider filtered the completion"}
	default:
		return protocol.FinishReason{}, fmt.Errorf("unsupported OpenAI-compatible finish reason %q", reason)
	}
	return protocol.FinishReason{Kind: kind}, nil
}

// LlmFailure supplies provider detail to the neutral Runtime error boundary.
func (e *ProviderError) LlmFailure() protocol.LlmFailure {
	code := e.Code
	if code == "" {
		code = e.Type
	}
	if code == "" {
		code = "UNKNOWN"
	}
	status := e.StatusCode
	return protocol.LlmFailure{Message: e.Error(), Code: code, Status: &status}
}
