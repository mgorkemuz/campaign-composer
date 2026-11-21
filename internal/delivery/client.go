// Package delivery sends approved campaign messages through the provider:
// queue workers, per-channel rate limits, retries and delivery receipts.
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Message is one message to one recipient.
type Message struct {
	Channel   string `json:"-"`
	To        string `json:"to"`
	Subject   string `json:"subject,omitempty"`
	Body      string `json:"body"`
	Reference string `json:"reference"` // our delivery ID; receipts carry it back
	// IdempotencyKey makes a retried send safe: the provider answers a repeated
	// key with its first result instead of sending again.
	IdempotencyKey string `json:"-"`
}

// ProviderError is a failed send. Temporary errors are retried.
type ProviderError struct {
	StatusCode int // 0 for a network error or timeout
	Code       string
	RetryAfter time.Duration
}

func (e *ProviderError) Error() string {
	if e.StatusCode == 0 {
		return "provider unreachable: " + e.Code
	}
	return fmt.Sprintf("provider returned HTTP %d (%s)", e.StatusCode, e.Code)
}

// Temporary reports whether retrying may succeed: rate limits, server errors and
// network problems. Other 4xx answers (e.g. an invalid number) will not change.
func (e *ProviderError) Temporary() bool {
	return e.StatusCode == 0 || e.StatusCode == http.StatusTooManyRequests ||
		e.StatusCode == http.StatusRequestTimeout || e.StatusCode >= 500
}

// Provider sends one message and returns the provider's message ID.
type Provider interface {
	Send(ctx context.Context, m Message) (messageID string, err error)
}

// Client talks to the provider's HTTP API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) Send(ctx context.Context, m Message) (string, error) {
	body, _ := json.Marshal(m)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/messages/"+m.Channel, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", m.IdempotencyKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", &ProviderError{Code: err.Error()}
	}
	defer resp.Body.Close()
	var out struct {
		MessageID string `json:"message_id"`
		Error     string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 {
		pe := &ProviderError{StatusCode: resp.StatusCode, Code: out.Error}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 {
			pe.RetryAfter = time.Duration(s) * time.Second
		}
		return "", pe
	}
	if out.MessageID == "" {
		return "", &ProviderError{StatusCode: resp.StatusCode, Code: "missing message_id"}
	}
	return out.MessageID, nil
}
