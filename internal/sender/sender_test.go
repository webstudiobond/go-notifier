package sender

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/webstudiobond/go-notifier/internal/config"
)

func mockFastRetryDelay(_ int) time.Duration {
	return time.Millisecond
}

func init() {
	retryDelay = mockFastRetryDelay
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestMatrixSender_Send(t *testing.T) {
	var receivedMessage bool
	var receivedUpload bool
	var receivedFileEvent bool

	mockTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/_matrix/media/v3/upload"):
			receivedUpload = true
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"content_uri":"mxc://example.org/abc123"}`)),
				Header:     make(http.Header),
			}, nil
		case strings.Contains(r.URL.Path, "/send/m.room.message/"):
			var raw map[string]any
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Errorf("failed to decode matrix payload: %v", err)
			}
			msgType, ok := raw["msgtype"].(string)
			if !ok {
				t.Errorf("missing string msgtype in payload")
			}
			switch msgType {
			case "m.text":
				receivedMessage = true
			case "m.file":
				receivedFileEvent = true
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"event_id":"$abc"}`)),
				Header:     make(http.Header),
			}, nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Header:     make(http.Header),
			}, nil
		}
	})

	sender := NewMatrixSender("https://matrix.example.com", "!room:example.com", "accesstoken123", "Matrix Site")
	sender.client = &http.Client{Transport: mockTransport}

	msg := &Message{
		Subject:  "Security Alert",
		BodyText: "Attack blocked",
		BodyHTML: "<b>Attack blocked</b>",
		Attachments: []Attachment{
			{
				Filename:      "sample.txt",
				MIMEType:      "text/plain",
				ContentBase64: base64.StdEncoding.EncodeToString([]byte("log data")),
			},
		},
	}

	err := sender.Send(context.Background(), msg, true)
	if err != nil {
		t.Fatalf("unexpected matrix send error: %v", err)
	}

	if !receivedMessage {
		t.Errorf("expected matrix text message to be received")
	}
	if !receivedUpload {
		t.Errorf("expected matrix media upload to be received")
	}
	if !receivedFileEvent {
		t.Errorf("expected matrix file event to be received")
	}
}

func TestNtfySender_Send(t *testing.T) {
	var receivedCount int

	mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		receivedCount++
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"id":"123"}`)),
			Header:     make(http.Header),
		}, nil
	})

	sender := NewNtfySender("https://ntfy.example.org", "alerts", "token123", "Ntfy Site")
	sender.client = &http.Client{Transport: mockTransport}

	msg := &Message{
		Subject:  "High Contention",
		BodyText: "CPU load exceeds threshold",
		Attachments: []Attachment{
			{
				Filename:      "log.txt",
				MIMEType:      "text/plain",
				ContentBase64: base64.StdEncoding.EncodeToString([]byte("sample log content")),
			},
		},
	}

	err := sender.Send(context.Background(), msg, true)
	if err != nil {
		t.Fatalf("unexpected ntfy send error: %v", err)
	}

	if receivedCount != 2 {
		t.Errorf("expected 2 ntfy requests (text + file), got %d", receivedCount)
	}
}

func TestSMTPSender_BuildMIMEMessage(t *testing.T) {
	cfg := config.SMTPConfig{
		Host:     "smtp.example.com",
		Port:     465,
		Mail:     "noreply@example.com",
		Password: "password123",
		FromName: "Support Team",
	}

	sender := NewSMTPSender(&cfg, "SMTP Site")

	msg := &Message{
		To:       []string{"recipient@example.net"},
		Subject:  "Invoice #1001",
		BodyText: "Please find your invoice attached.",
		BodyHTML: "<p>Please find your invoice attached.</p>",
		ReplyTo:  "billing@example.org",
		Attachments: []Attachment{
			{
				Filename:      "invoice.pdf",
				MIMEType:      "application/pdf",
				ContentBase64: base64.StdEncoding.EncodeToString([]byte("binary pdf content")),
			},
		},
	}

	raw := string(sender.buildMIMEMessage(msg))

	if !strings.Contains(raw, "From: Support Team <noreply@example.com>") {
		t.Errorf("expected From header, got:\n%s", raw)
	}
	if !strings.Contains(raw, "To: recipient@example.net") {
		t.Errorf("expected To header, got:\n%s", raw)
	}
	if !strings.Contains(raw, "Reply-To: billing@example.org") {
		t.Errorf("expected Reply-To header, got:\n%s", raw)
	}
	if !strings.Contains(raw, "multipart/mixed") {
		t.Errorf("expected multipart/mixed for attachments, got:\n%s", raw)
	}
	if !strings.Contains(raw, "invoice.pdf") {
		t.Errorf("expected invoice.pdf in attachment part, got:\n%s", raw)
	}
}

func TestSMTPSender_BuildMIMEMessage_NonASCII(t *testing.T) {
	cfg := config.SMTPConfig{
		Host:     "smtp.example.org",
		Port:     587,
		Mail:     "noreply@example.org",
		FromName: "Поддержка",
	}

	sender := NewSMTPSender(&cfg, "Сайт")

	msg := &Message{
		To:       []string{"user@example.net"},
		Subject:  "Тестовое уведомление",
		BodyText: "Тестовое тело сообщения",
	}

	raw := string(sender.buildMIMEMessage(msg))

	if !strings.Contains(raw, "=?UTF-8?b?") {
		t.Errorf("expected RFC 2047 encoded subject, got:\n%s", raw)
	}
}

func TestSender_DefaultRetryDelay(t *testing.T) {
	d0 := defaultRetryDelay(0)
	if d0 != 200*time.Millisecond {
		t.Fatalf("expected 200ms, got %v", d0)
	}
	d2 := defaultRetryDelay(2)
	if d2 != 600*time.Millisecond {
		t.Fatalf("expected 600ms, got %v", d2)
	}
}
