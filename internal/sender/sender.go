// Package sender defines drivers for outbound notification and email delivery.
package sender

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func defaultRetryDelay(attempt int) time.Duration {
	return time.Duration(attempt+1) * 200 * time.Millisecond
}

var (
	jsonMarshal               = json.Marshal
	newHTTPRequestWithContext = http.NewRequestWithContext
	defaultMaxAttempts        = 3
	retryDelay                = defaultRetryDelay
)

// Attachment represents a file payload encoded in Base64.
type Attachment struct {
	Filename      string `json:"filename"`
	MIMEType      string `json:"mime_type,omitempty"`
	ContentBase64 string `json:"content_base64"`
}

// Message represents the canonical notification and mail payload.
type Message struct {
	Subject     string       `json:"subject"`
	BodyText    string       `json:"body_text,omitempty"`
	BodyHTML    string       `json:"body_html,omitempty"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	To          []string     `json:"to"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Sender defines the contract implemented by each notification channel.
type Sender interface {
	Name() string
	Send(ctx context.Context, msg *Message, sendAttachments bool) error
}
