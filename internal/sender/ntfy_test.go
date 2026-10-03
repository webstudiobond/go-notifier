package sender

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNtfySender_Name(t *testing.T) {
	sender := NewNtfySender("https://ntfy-name.example.com", "alerts-name", "token-name", "Site Ntfy")
	if sender.Name() != "ntfy" {
		t.Fatalf("expected Name 'ntfy', got %q", sender.Name())
	}
}

func TestNtfySender_FormattingAndBranches(t *testing.T) {
	tests := []struct {
		name        string
		bodyText    string
		bodyHTML    string
		wantContent string
		attach      bool
	}{
		{
			name:        "body_html_fallback_when_body_text_empty",
			bodyText:    "",
			bodyHTML:    "System alert HTML",
			wantContent: "System alert HTML",
			attach:      false,
		},
		{
			name:        "body_text_preferred_over_html",
			bodyText:    "Plain alert text",
			bodyHTML:    "<p>Html text</p>",
			wantContent: "Plain alert text",
			attach:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var recordedBody string
			var recordedTitle string
			mockTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				bodyBytes, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("failed to read body: %v", err)
				}
				recordedBody = string(bodyBytes)
				recordedTitle = r.Header.Get("Title")
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"id":"ok-fmt"}`)),
					Header:     make(http.Header),
				}, nil
			})

			sender := NewNtfySender("https://ntfy-fmt.example.org", "topic-fmt", "secret-fmt", "Site Ntfy")
			sender.client = &http.Client{Transport: mockTransport}

			msg := &Message{
				Subject:  "Subject A",
				BodyText: tt.bodyText,
				BodyHTML: tt.bodyHTML,
			}

			err := sender.Send(context.Background(), msg, tt.attach)
			if err != nil {
				t.Fatalf("unexpected Send error: %v", err)
			}
			if recordedBody != tt.wantContent {
				t.Errorf("expected body %q, got %q", tt.wantContent, recordedBody)
			}
			if recordedTitle != "[Site Ntfy] Subject A" {
				t.Errorf("expected title '[Site Ntfy] Subject A', got %q", recordedTitle)
			}
		})
	}
}

func TestNtfySender_Send_ErrorBranches(t *testing.T) {
	t.Run("send_text_message_error_aborts", func(t *testing.T) {
		mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("server failure")),
				Header:     make(http.Header),
			}, nil
		})

		sender := NewNtfySender("https://ntfy-err1.example.net", "topic-err1", "token-err1", "Site Err")
		sender.client = &http.Client{Transport: mockTransport}

		msg := &Message{
			Subject:  "Alert Text Error",
			BodyText: "Some text payload",
		}

		err := sender.Send(context.Background(), msg, false)
		if err == nil || !strings.Contains(err.Error(), "ntfy returned status 500") {
			t.Fatalf("expected sendTextMessage error, got %v", err)
		}
	})

	t.Run("send_attachment_error_aborts", func(t *testing.T) {
		mockTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Filename") != "" {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader("invalid attachment payload")),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"id":"ok-att"}`)),
				Header:     make(http.Header),
			}, nil
		})

		sender := NewNtfySender("https://ntfy-err2.example.org", "topic-err2", "token-err2", "Site Err2")
		sender.client = &http.Client{Transport: mockTransport}

		msg := &Message{
			Subject:  "Alert With File",
			BodyText: "File notification",
			Attachments: []Attachment{
				{
					Filename:      "report.tsv",
					ContentBase64: base64.StdEncoding.EncodeToString([]byte("tsv\tdata")),
				},
			},
		}

		err := sender.Send(context.Background(), msg, true)
		if err == nil || !strings.Contains(err.Error(), "ntfy returned status 400") {
			t.Fatalf("expected sendFile error, got %v", err)
		}
	})
}

func TestNtfySender_SendTextMessage_Branches(t *testing.T) {
	t.Run("without_token_omits_authorization", func(t *testing.T) {
		var authHeader string
		mockTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			authHeader = r.Header.Get("Authorization")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"id":"notoken"}`)),
				Header:     make(http.Header),
			}, nil
		})

		sender := NewNtfySender("https://ntfy-notoken.example.com", "public-topic", "", "Site Public")
		sender.client = &http.Client{Transport: mockTransport}

		err := sender.sendTextMessage(context.Background(), "Notice Title", "Notice Content")
		if err != nil {
			t.Fatalf("unexpected sendTextMessage error: %v", err)
		}
		if authHeader != "" {
			t.Errorf("expected empty Authorization header, got %q", authHeader)
		}
	})

	t.Run("request_creation_failure", func(t *testing.T) {
		badSender := NewNtfySender("https://ntfy-bad1.example.com/bad\x7fpath", "badtopic1", "badtok1", "Site Bad1")
		err := badSender.sendTextMessage(context.Background(), "Notice Bad", "Content Bad")
		if err == nil || !strings.Contains(err.Error(), "create ntfy request") {
			t.Fatalf("expected request creation error, got %v", err)
		}
	})
}

func TestNtfySender_SendFile_Branches(t *testing.T) {
	t.Run("corrupted_base64_returns_error", func(t *testing.T) {
		sender := NewNtfySender("https://ntfy-file1.example.net", "topic-file", "token-f1", "Site File1")
		att := Attachment{
			Filename:      "bad.bin",
			ContentBase64: "!!!not-valid-base64!!!",
		}
		err := sender.sendFile(context.Background(), "Title Corrupted", att)
		if err == nil || !strings.Contains(err.Error(), "decode ntfy attachment") {
			t.Fatalf("expected base64 decode error, got %v", err)
		}
	})

	t.Run("without_token_omits_authorization", func(t *testing.T) {
		var authHeader string
		var filenameHeader string
		mockTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			authHeader = r.Header.Get("Authorization")
			filenameHeader = r.Header.Get("Filename")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"id":"file-ok"}`)),
				Header:     make(http.Header),
			}, nil
		})

		sender := NewNtfySender("https://ntfy-file2.example.org", "topic-file2", "", "Site File2")
		sender.client = &http.Client{Transport: mockTransport}

		att := Attachment{
			Filename:      "archive.tar",
			ContentBase64: base64.StdEncoding.EncodeToString([]byte("tarball")),
		}
		err := sender.sendFile(context.Background(), "Backup Title", att)
		if err != nil {
			t.Fatalf("unexpected sendFile error: %v", err)
		}
		if authHeader != "" {
			t.Errorf("expected empty Authorization header, got %q", authHeader)
		}
		if filenameHeader != "archive.tar" {
			t.Errorf("expected Filename header 'archive.tar', got %q", filenameHeader)
		}
	})

	t.Run("request_creation_failure", func(t *testing.T) {
		badSender := NewNtfySender("https://ntfy-bad2.example.net/bad\x7fpath", "badtopic2", "badtok2", "Site Bad2")
		att := Attachment{
			Filename:      "bundle.tar",
			ContentBase64: base64.StdEncoding.EncodeToString([]byte("bundle-data")),
		}
		err := badSender.sendFile(context.Background(), "Backup Bad", att)
		if err == nil || !strings.Contains(err.Error(), "create ntfy file request") {
			t.Fatalf("expected file request creation error, got %v", err)
		}
	})
}

func TestNtfySender_ExecuteWithRetry_TransportCases(t *testing.T) {
	t.Run("reqFactory_error_returns_immediately", func(t *testing.T) {
		sender := NewNtfySender("https://ntfy-retry1.example.com", "top1", "tok1", "Site R1")
		err := sender.executeWithRetry(context.Background(), func() (*http.Request, error) {
			return nil, errors.New("factory failure")
		})
		if err == nil || !strings.Contains(err.Error(), "factory failure") {
			t.Fatalf("expected factory error, got %v", err)
		}
	})

	t.Run("transport_retry_success", func(t *testing.T) {
		attempts := 0
		mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("socket timeout")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("ok-retry")),
				Header:     make(http.Header),
			}, nil
		})

		sender := NewNtfySender("https://ntfy-retry2.example.org", "top2", "tok2", "Site R2")
		sender.client = &http.Client{Transport: mockTransport}

		err := sender.sendTextMessage(context.Background(), "retry title", "retry content")
		if err != nil {
			t.Fatalf("expected retry to succeed, got %v", err)
		}
		if attempts != 2 {
			t.Fatalf("expected 2 attempts, got %d", attempts)
		}
	})

	t.Run("context_cancelled_during_transport_retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			cancel()
			return nil, errors.New("network failure")
		})

		sender := NewNtfySender("https://ntfy-retry3.example.net", "top3", "tok3", "Site R3")
		sender.client = &http.Client{Transport: mockTransport}

		err := sender.sendTextMessage(ctx, "cancel title", "cancel content")
		if err == nil || !strings.Contains(err.Error(), "context cancelled during retry") {
			t.Fatalf("expected context cancellation error, got %v", err)
		}
	})
}

func TestNtfySender_ExecuteWithRetry_ResponseCases(t *testing.T) {
	t.Run("body_read_error", func(t *testing.T) {
		mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       &errReader{},
				Header:     make(http.Header),
			}, nil
		})

		sender := NewNtfySender("https://ntfy-retry4.example.com", "top4", "tok4", "Site R4")
		sender.client = &http.Client{Transport: mockTransport}

		err := sender.sendTextMessage(context.Background(), "readerr title", "readerr content")
		if err == nil || !strings.Contains(err.Error(), "unable to read response") {
			t.Fatalf("expected read error fallback, got %v", err)
		}
	})

	t.Run("close_error_on_status_ok", func(t *testing.T) {
		mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &errCloser{Reader: strings.NewReader("ok response")},
				Header:     make(http.Header),
			}, nil
		})

		sender := NewNtfySender("https://ntfy-retry5.example.org", "top5", "tok5", "Site R5")
		sender.client = &http.Client{Transport: mockTransport}

		err := sender.sendTextMessage(context.Background(), "closeok title", "closeok content")
		if err == nil || !strings.Contains(err.Error(), "close ntfy response") {
			t.Fatalf("expected close error on 200 OK, got %v", err)
		}
	})

	t.Run("close_error_on_status_error", func(t *testing.T) {
		mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       &errCloser{Reader: strings.NewReader("unavailable")},
				Header:     make(http.Header),
			}, nil
		})

		sender := NewNtfySender("https://ntfy-retry6.example.net", "top6", "tok6", "Site R6")
		sender.client = &http.Client{Transport: mockTransport}

		err := sender.sendTextMessage(context.Background(), "closesvc title", "closesvc content")
		if err == nil || !strings.Contains(err.Error(), "close simulation error") {
			t.Fatalf("expected close error on status error, got %v", err)
		}
	})

	t.Run("context_cancelled_during_status_retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			cancel()
			return &http.Response{
				StatusCode: http.StatusGatewayTimeout,
				Body:       io.NopCloser(strings.NewReader("gateway timeout")),
				Header:     make(http.Header),
			}, nil
		})

		sender := NewNtfySender("https://ntfy-retry7.example.com", "top7", "tok7", "Site R7")
		sender.client = &http.Client{Transport: mockTransport}

		err := sender.sendTextMessage(ctx, "timeout title", "timeout content")
		if err == nil || !strings.Contains(err.Error(), "context cancelled during retry") {
			t.Fatalf("expected context cancellation error, got %v", err)
		}
	})
}

func TestNtfySender_ExecuteWithRetry_ZeroAttempts(t *testing.T) {
	origMaxAttempts := defaultMaxAttempts
	defer func() { defaultMaxAttempts = origMaxAttempts }()
	defaultMaxAttempts = 0

	sender := NewNtfySender("https://ntfy-zero.example.org", "top-zero", "tok-zero", "Site Zero")
	err := sender.executeWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodGet, "https://target-zero.example.org", http.NoBody)
	})
	if err == nil || !strings.Contains(err.Error(), "ntfy request failed with unknown error") {
		t.Fatalf("expected unknown error on zero attempts, got %v", err)
	}
}
