package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// postmarkEndpoint is Postmark's single-email send API.
const postmarkEndpoint = "https://api.postmarkapp.com/email"

// PostmarkSender sends over Postmark's HTTP API directly, hand-rolled rather than
// through their SDK — the same choice ADR 0007 made for other external calls in this
// service, to keep the dependency surface to what this one call actually needs.
type PostmarkSender struct {
	apiKey       string
	fromAddress  string
	httpClient   *http.Client
	postmarkHost string // overridable in tests; defaults to postmarkEndpoint
}

// NewPostmarkSender returns a Sender backed by Postmark. apiKey is the server token
// from the Postmark server's API Tokens tab; fromAddress must be a Sender Signature
// already verified with Postmark, or every send is rejected regardless of how correct
// everything else is.
func NewPostmarkSender(apiKey, fromAddress string) *PostmarkSender {
	return &PostmarkSender{
		apiKey:       apiKey,
		fromAddress:  fromAddress,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		postmarkHost: postmarkEndpoint,
	}
}

type postmarkRequest struct {
	From     string `json:"From"`
	To       string `json:"To"`
	Subject  string `json:"Subject"`
	HTMLBody string `json:"HtmlBody"`
	// MessageStream is left at Postmark's default ("outbound") rather than set
	// explicitly. If Phase 4's transactional volume ever needs its own stream
	// (separate from any marketing stream Postmark account might add later), set it
	// here — deliberately not anticipated before there is a second stream to
	// separate it from.
}

// postmarkErrorResponse is Postmark's error body shape. ErrorCode distinguishes a
// permanent rejection (e.g. 300: invalid address, 406: inactive/hard-bounced
// recipient) from a transient one (e.g. rate limiting) — see
// https://postmarkapp.com/developer/api/overview#error-codes. This package treats
// every non-2xx as a failure the caller may retry (docs/adr/0010's at-least-once
// stance already tolerates a redundant retry); it does not special-case which
// ErrorCode values are worth giving up on immediately; the attempt cap in
// internal/store bounds the cost either way.
type postmarkErrorResponse struct {
	ErrorCode int    `json:"ErrorCode"`
	Message   string `json:"Message"`
}

// Send implements Sender.
func (p *PostmarkSender) Send(ctx context.Context, to, subject, htmlBody string) error {
	body, err := json.Marshal(postmarkRequest{
		From: p.fromAddress, To: to, Subject: subject, HTMLBody: htmlBody,
	})
	if err != nil {
		return fmt.Errorf("encode postmark request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.postmarkHost, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build postmark request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Postmark-Server-Token", p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("postmark request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var perr postmarkErrorResponse
	if err := json.Unmarshal(respBody, &perr); err == nil && perr.Message != "" {
		return fmt.Errorf("postmark rejected the send (code %d): %s", perr.ErrorCode, perr.Message)
	}
	return fmt.Errorf("postmark returned status %d", resp.StatusCode)
}
