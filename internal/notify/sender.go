// Package notify sends lifecycle email (spec §7) for the events that need nothing
// from Phase 2: schedule created, and paused / resumed / canceled. Pre-billing
// notice, order-placed, and the dunning ladder all need Phase 2's order/payment
// pipeline and are not built here — see docs/adr/0007.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Sender delivers one email. Message carries rendered content only — no template
// concerns cross this boundary, so a test double never needs to know about
// html/template.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Message is one rendered, ready-to-send email.
type Message struct {
	To       string
	Subject  string
	HTMLBody string
	TextBody string
}

// PostmarkSender sends via Postmark's HTTP API directly — no SDK dependency, same
// restraint this project already applies to third-party code (docs/RECOMMENDED_ACTIONS.md's
// reasoning about GitHub Actions applies equally to a Go dependency for what is, at
// bottom, one POST request). Spec §7: "same client pattern as PartnerOS" — PartnerOS
// isn't reachable, so this is a provisional decision, recorded in docs/adr/0007
// the same way ADR 0002/0003 recorded the queue and migration tool choices.
type PostmarkSender struct {
	ServerToken string
	FromAddress string

	// HTTPClient is injected so tests never make a real network call. Defaults to
	// http.DefaultClient when nil.
	HTTPClient *http.Client

	// Endpoint overrides the Postmark API URL. Defaults to the real one when empty
	// — the only reason this is a field rather than a package constant is so a
	// test can point it at an httptest.Server instead.
	Endpoint string
}

const defaultPostmarkEndpoint = "https://api.postmarkapp.com/email"

type postmarkRequest struct {
	From          string `json:"From"`
	To            string `json:"To"`
	Subject       string `json:"Subject"`
	HTMLBody      string `json:"HtmlBody"`
	TextBody      string `json:"TextBody"`
	MessageStream string `json:"MessageStream"`
}

type postmarkResponse struct {
	ErrorCode int    `json:"ErrorCode"`
	Message   string `json:"Message"`
	MessageID string `json:"MessageID"`
}

// Send posts one message to Postmark. Both a non-2xx HTTP status and a non-zero
// Postmark ErrorCode are treated as failures — Postmark can return either
// depending on the error class, and neither means the email went out.
func (p *PostmarkSender) Send(ctx context.Context, msg Message) error {
	body, err := json.Marshal(postmarkRequest{
		From:          p.FromAddress,
		To:            msg.To,
		Subject:       msg.Subject,
		HTMLBody:      msg.HTMLBody,
		TextBody:      msg.TextBody,
		MessageStream: "outbound",
	})
	if err != nil {
		return fmt.Errorf("encode postmark request: %w", err)
	}

	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = defaultPostmarkEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build postmark request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Postmark-Server-Token", p.ServerToken)

	client := p.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send to postmark: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var parsed postmarkResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("decode postmark response (status %d): %w", resp.StatusCode, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("postmark returned HTTP %d: %s", resp.StatusCode, parsed.Message)
	}
	if parsed.ErrorCode != 0 {
		return fmt.Errorf("postmark error %d: %s", parsed.ErrorCode, parsed.Message)
	}
	return nil
}
