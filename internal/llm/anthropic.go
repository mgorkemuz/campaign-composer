package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Anthropic calls Claude through the official SDK. Every call streams, so long
// answers never hit HTTP timeouts and the UI can show text as it arrives.
type Anthropic struct {
	client anthropic.Client
	model  string
	effort anthropic.BetaOutputConfigEffort
	apiKey string // only to scrub it from error messages
	// workspaceID is sent as anthropic-workspace-id when set.
	workspaceID string
}

type AnthropicOptions struct {
	APIKey string
	Model  string
	// Effort is "low", "medium" or "high". The tasks here are short and
	// structured, so low keeps them fast and cheap.
	Effort string
	// WorkspaceID is required when the key is not scoped to a workspace.
	WorkspaceID string
	// BaseURL and HTTPClient let tests point the SDK at a local server.
	BaseURL    string
	HTTPClient *http.Client
}

func NewAnthropic(o AnthropicOptions) *Anthropic {
	opts := []option.RequestOption{
		option.WithAPIKey(o.APIKey),
		// GenerateWithRetry is the one retry policy, so it can honor Retry-After
		// and never repeat a half-streamed answer.
		option.WithMaxRetries(0),
	}
	if o.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(o.BaseURL))
	}
	if o.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(o.HTTPClient))
	}
	if o.Effort == "" {
		o.Effort = "low"
	}
	return &Anthropic{
		client:      anthropic.NewClient(opts...),
		model:       o.Model,
		effort:      anthropic.BetaOutputConfigEffort(o.Effort),
		apiKey:      o.APIKey,
		workspaceID: o.WorkspaceID,
	}
}

func (a *Anthropic) Name() string { return "claude:" + a.model }

// fallbackModels support the server-side refusal fallback: a request the model
// declines is answered by a fallback model in the same call. Haiku does not.
var fallbackModels = map[string]bool{
	"claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true,
	"claude-fable-5-2": true, "claude-fable-5-1": true,
}

const defaultMaxTokens = 8000

func (a *Anthropic) Generate(ctx context.Context, req Request, onText func(string)) (Response, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	params := anthropic.BetaMessageNewParams{
		Model:        a.model,
		MaxTokens:    int64(maxTokens),
		Messages:     toParams(req.Messages),
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: a.effort},
	}
	if a.workspaceID != "" {
		params.WorkspaceID = anthropic.String(a.workspaceID)
	}
	if req.System != "" {
		params.System = []anthropic.BetaTextBlockParam{{Text: req.System}}
	}
	if req.Schema != nil {
		params.OutputConfig.Format = anthropic.BetaJSONOutputFormatParam{Schema: req.Schema}
	}
	if fallbackModels[a.model] {
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
		params.Betas = append(params.Betas, anthropic.AnthropicBetaServerSideFallback2026_07_01)
	}

	stream := a.client.Beta.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	var msg anthropic.BetaMessage
	streamed := false
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return Response{}, fmt.Errorf("claude stream: %w", err)
		}
		if d, ok := ev.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
			if td, ok := d.Delta.AsAny().(anthropic.BetaTextDelta); ok && td.Text != "" {
				streamed = true
				if onText != nil {
					onText(td.Text)
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return Response{}, a.mapErr(ctx, err, streamed)
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if tb, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(tb.Text)
		}
	}
	resp := Response{
		Text:       text.String(),
		Model:      string(msg.Model),
		StopReason: string(msg.StopReason),
		Usage:      Usage{InputTokens: int(msg.Usage.InputTokens), OutputTokens: int(msg.Usage.OutputTokens)},
	}
	switch resp.StopReason {
	case "refusal":
		return resp, ErrRefused
	case "max_tokens":
		return resp, ErrTruncated
	}
	return resp, nil
}

// mapErr turns SDK errors into StatusErrors (retryable when temporary) as long
// as no text has streamed yet. After that, a retry would repeat output, so the
// error is returned as it is.
func (a *Anthropic) mapErr(ctx context.Context, err error, streamed bool) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if streamed {
		return fmt.Errorf("claude stream broke after output started: %s", cleanMessage(err.Error(), a.apiKey))
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		se := &StatusError{Provider: "claude", StatusCode: apiErr.StatusCode, Message: cleanMessage(apiErr.Error(), a.apiKey)}
		if apiErr.Response != nil {
			se.RetryAfter = parseRetryAfter(apiErr.Response.Header.Get("Retry-After"))
		}
		return se
	}
	// A connection error or an error event (e.g. overloaded) before any
	// output: nothing was shown yet, so it is safe to try again.
	return &StatusError{Provider: "claude", StatusCode: http.StatusServiceUnavailable, Message: cleanMessage(err.Error(), a.apiKey)}
}

func toParams(msgs []Message) []anthropic.BetaMessageParam {
	out := make([]anthropic.BetaMessageParam, 0, len(msgs))
	for _, m := range msgs {
		block := anthropic.NewBetaTextBlock(m.Text)
		if m.Role == RoleAssistant {
			out = append(out, anthropic.BetaMessageParam{OfAssistant: &anthropic.BetaAssistantMessageParam{
				Content: []anthropic.BetaContentBlockParamUnion{block},
			}})
			continue
		}
		out = append(out, anthropic.NewBetaUserMessage(block))
	}
	return out
}
