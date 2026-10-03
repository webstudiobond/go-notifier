package sender

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type errReader struct{}

func (e *errReader) Read(_ []byte) (int, error) {
	return 0, errors.New("read simulation error")
}

func (e *errReader) Close() error {
	return nil
}

type errCloser struct {
	io.Reader
}

func (e *errCloser) Close() error {
	return errors.New("close simulation error")
}

func TestMatrixSender_Name(t *testing.T) {
	sender := NewMatrixSender("https://matrix.example.com", "!room:example.com", "token123", "Site")
	if sender.Name() != "matrix" {
		t.Fatalf("expected Name 'matrix', got %q", sender.Name())
	}
}

func TestMatrixSender_FormatMessage(t *testing.T) {
	sender := NewMatrixSender("https://matrix.example.com", "!room:example.com", "token123", "Site Alpha")

	tests := []struct {
		name      string
		wantPlain string
		wantFmt   string
		msg       Message
	}{
		{
			name: "body_html_fallback_when_body_text_empty",
			msg: Message{
				Subject:  "Notice 1",
				BodyText: "",
				BodyHTML: "<em>html content</em>",
			},
			wantPlain: "html content",
			wantFmt:   "<em>",
		},
		{
			name: "body_text_multiline_escaped_when_html_empty",
			msg: Message{
				Subject:  "Notice 2",
				BodyText: "First line\nSecond <tag>",
				BodyHTML: "",
			},
			wantPlain: "First line",
			wantFmt:   "&lt;tag&gt;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain, formatted := sender.formatMessage(&tt.msg)
			if !strings.Contains(plain, tt.wantPlain) {
				t.Fatalf("expected plain to contain %q, got %q", tt.wantPlain, plain)
			}
			if !strings.Contains(formatted, tt.wantFmt) {
				t.Fatalf("expected formatted to contain %q, got %q", tt.wantFmt, formatted)
			}
		})
	}
}

func TestMatrixSender_UploadMedia_EdgeCases(t *testing.T) {
	tests := []struct {
		mockResp func(r *http.Request) (*http.Response, error)
		name     string
		att      Attachment
		wantErr  bool
	}{
		{
			name: "empty_mimetype_defaults_to_octet_stream",
			att: Attachment{
				Filename: "blob.bin",
				MIMEType: "",
			},
			mockResp: func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Content-Type") != "application/octet-stream" {
					t.Errorf("expected Content-Type application/octet-stream, got %s", r.Header.Get("Content-Type"))
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"content_uri":"mxc://example.org/blob1"}`)),
					Header:     make(http.Header),
				}, nil
			},
			wantErr: false,
		},
		{
			name: "invalid_json_upload_response",
			att: Attachment{
				Filename: "data.bin",
				MIMEType: "image/png",
			},
			mockResp: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("invalid-json-body")),
					Header:     make(http.Header),
				}, nil
			},
			wantErr: true,
		},
		{
			name: "missing_content_uri_in_response",
			att: Attachment{
				Filename: "doc.bin",
				MIMEType: "application/xml",
			},
			mockResp: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"other_field":"val"}`)),
					Header:     make(http.Header),
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := NewMatrixSender("https://matrix.example.net", "!room:example.net", "token456", "Site Beta")
			sender.client = &http.Client{Transport: roundTripFunc(tt.mockResp)}

			_, err := sender.uploadMedia(context.Background(), tt.att, []byte("payload"))
			if (err != nil) != tt.wantErr {
				t.Fatalf("uploadMedia() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestMatrixSender_SendFile_EdgeCases(t *testing.T) {
	tests := []struct {
		mockResp func(r *http.Request) (*http.Response, error)
		name     string
		att      Attachment
		wantErr  bool
	}{
		{
			name: "corrupted_base64_attachment",
			att: Attachment{
				Filename:      "bad.bin",
				MIMEType:      "image/jpeg",
				ContentBase64: "invalid!!base64",
			},
			mockResp: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(nil)),
					Header:     make(http.Header),
				}, nil
			},
			wantErr: true,
		},
		{
			name: "upload_media_failure_propagates_error",
			att: Attachment{
				Filename:      "fail.bin",
				MIMEType:      "application/json",
				ContentBase64: base64.StdEncoding.EncodeToString([]byte("data")),
			},
			mockResp: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader("upload error")),
					Header:     make(http.Header),
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := NewMatrixSender("https://matrix.example.org", "!room:example.org", "token789", "Site Gamma")
			sender.client = &http.Client{Transport: roundTripFunc(tt.mockResp)}

			err := sender.sendFile(context.Background(), tt.att)
			if (err != nil) != tt.wantErr {
				t.Fatalf("sendFile() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestMatrixSender_HandleResponse_EdgeCases(t *testing.T) {
	sender := NewMatrixSender("https://matrix.example.com", "!room:example.com", "token123", "Site Delta")

	t.Run("body_read_error", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       &errReader{},
		}
		err := sender.handleResponse(resp, nil)
		if err == nil || !strings.Contains(err.Error(), "unable to read response") {
			t.Fatalf("expected read error fallback, got %v", err)
		}
	})

	t.Run("close_error_on_status_ok", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errCloser{Reader: strings.NewReader("ok body")},
		}
		err := sender.handleResponse(resp, nil)
		if err == nil || !strings.Contains(err.Error(), "close matrix response") {
			t.Fatalf("expected close error on OK status, got %v", err)
		}
	})

	t.Run("close_error_on_status_error", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       &errCloser{Reader: strings.NewReader("bad request")},
		}
		err := sender.handleResponse(resp, nil)
		if err == nil || !strings.Contains(err.Error(), "close simulation error") {
			t.Fatalf("expected close error on bad status, got %v", err)
		}
	})
}

func TestMatrixSender_Send_ErrorBranches(t *testing.T) {
	t.Run("send_text_message_error_aborts", func(t *testing.T) {
		mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(strings.NewReader("unauthorized")),
				Header:     make(http.Header),
			}, nil
		})

		sender := NewMatrixSender("https://matrix.example.com", "!room:example.com", "token123", "Site Epsilon")
		sender.client = &http.Client{Transport: mockTransport}

		msg := &Message{
			Subject:  "Alert",
			BodyText: "Some text",
		}
		err := sender.Send(context.Background(), msg, false)
		if err == nil {
			t.Fatal("expected Send error on text message failure")
		}
	})

	t.Run("send_attachment_error_aborts", func(t *testing.T) {
		mockTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.Contains(r.URL.Path, "/send/m.room.message/") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"event_id":"$ev1"}`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader("upload forbidden")),
				Header:     make(http.Header),
			}, nil
		})

		sender := NewMatrixSender("https://matrix.example.com", "!room:example.com", "token123", "Site Zeta")
		sender.client = &http.Client{Transport: mockTransport}

		msg := &Message{
			Subject:  "Alert with Attachment",
			BodyText: "Some text",
			Attachments: []Attachment{
				{
					Filename:      "att.bin",
					MIMEType:      "text/csv",
					ContentBase64: base64.StdEncoding.EncodeToString([]byte("data")),
				},
			},
		}
		err := sender.Send(context.Background(), msg, true)
		if err == nil {
			t.Fatal("expected Send error on attachment failure")
		}
	})
}

func TestMatrixSender_ExecuteWithRetry_ContextCancelled(t *testing.T) {
	t.Run("context_cancelled_during_transport_error_retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			cancel()
			return nil, errors.New("network down")
		})

		sender := NewMatrixSender("https://matrix.example.com", "!room:example.com", "token123", "Site Eta")
		sender.client = &http.Client{Transport: mockTransport}

		err := sender.sendTextMessage(ctx, "plain", "formatted")
		if err == nil || !strings.Contains(err.Error(), "context cancelled during retry") {
			t.Fatalf("expected context cancellation error, got %v", err)
		}
	})

	t.Run("context_cancelled_during_response_retry", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			cancel()
			return &http.Response{
				StatusCode: http.StatusGatewayTimeout,
				Body:       io.NopCloser(strings.NewReader("gateway timeout")),
				Header:     make(http.Header),
			}, nil
		})

		sender := NewMatrixSender("https://matrix.example.com", "!room:example.com", "token123", "Site Theta")
		sender.client = &http.Client{Transport: mockTransport}

		err := sender.sendTextMessage(ctx, "plain", "formatted")
		if err == nil || !strings.Contains(err.Error(), "context cancelled during retry") {
			t.Fatalf("expected context cancellation error, got %v", err)
		}
	})
}

func TestMatrixSender_ExecuteWithRetry_TransportRetrySuccess(t *testing.T) {
	attempts := 0
	mockTransport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("temporary network glitch")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"event_id":"$ev_ok"}`)),
			Header:     make(http.Header),
		}, nil
	})

	sender := NewMatrixSender("https://matrix.example.com", "!room:example.com", "token123", "Site Iota")
	sender.client = &http.Client{Transport: mockTransport}

	err := sender.sendTextMessage(context.Background(), "plain text", "formatted text")
	if err != nil {
		t.Fatalf("expected retry to succeed on second attempt, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestMatrixSender_RequestCreationErrors(t *testing.T) {
	badSender := NewMatrixSender("https://matrix.example.com/bad\x7fpath", "!room:example.com", "token123", "Site Kappa")
	ctx := context.Background()

	if err := badSender.sendTextMessage(ctx, "plain", "formatted"); err == nil {
		t.Fatal("expected error with invalid URL characters in sendTextMessage")
	}

	att := Attachment{
		Filename:      "test.txt",
		MIMEType:      "text/markdown",
		ContentBase64: base64.StdEncoding.EncodeToString([]byte("data")),
	}
	if _, err := badSender.uploadMedia(ctx, att, []byte("data")); err == nil {
		t.Fatal("expected error with invalid URL characters in uploadMedia")
	}

	if err := badSender.sendFile(ctx, att); err == nil {
		t.Fatal("expected error with invalid URL characters in sendFile")
	}
}

func mockFailingMarshal(_ any) ([]byte, error) {
	return nil, errors.New("simulated marshal failure")
}

func mockFailingSendRequestWithContext(ctx context.Context, method, targetURL string, body io.Reader) (*http.Request, error) {
	if strings.Contains(targetURL, "/send/") {
		return nil, errors.New("simulated file event request creation failure")
	}
	return http.NewRequestWithContext(ctx, method, targetURL, body)
}

func TestMatrixSender_SendTextMessage_MarshalFailure(t *testing.T) {
	origMarshal := jsonMarshal
	defer func() { jsonMarshal = origMarshal }()
	jsonMarshal = mockFailingMarshal

	sender := NewMatrixSender("https://lambda.example.net", "!room:example.net", "token-lambda", "Site Lambda")
	err := sender.sendTextMessage(context.Background(), "plain text", "formatted text")
	if err == nil || !strings.Contains(err.Error(), "marshal matrix message") {
		t.Fatalf("expected marshal matrix message error, got %v", err)
	}
}

func TestMatrixSender_SendFile_MarshalFailure(t *testing.T) {
	sender := NewMatrixSender("https://mu.example.org", "!room:example.org", "token-mu", "Site Mu")
	sender.client = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"content_uri":"mxc://media.example.org/1"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	origMarshal := jsonMarshal
	defer func() { jsonMarshal = origMarshal }()
	jsonMarshal = mockFailingMarshal

	att := Attachment{
		Filename:      "doc1.zip",
		MIMEType:      "application/zip",
		ContentBase64: base64.StdEncoding.EncodeToString([]byte("data1")),
	}

	err := sender.sendFile(context.Background(), att)
	if err == nil || !strings.Contains(err.Error(), "marshal matrix file message") {
		t.Fatalf("expected marshal matrix file message error, got %v", err)
	}
}

func TestMatrixSender_SendFile_RequestCreationFailure(t *testing.T) {
	sender := NewMatrixSender("https://nu.example.net", "!room:example.net", "token-nu", "Site Nu")
	sender.client = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"content_uri":"mxc://media.example.net/2"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	origNewRequest := newHTTPRequestWithContext
	defer func() { newHTTPRequestWithContext = origNewRequest }()
	newHTTPRequestWithContext = mockFailingSendRequestWithContext

	att := Attachment{
		Filename:      "doc2.webp",
		MIMEType:      "image/webp",
		ContentBase64: base64.StdEncoding.EncodeToString([]byte("data2")),
	}

	err := sender.sendFile(context.Background(), att)
	if err == nil || !strings.Contains(err.Error(), "create matrix file event request") {
		t.Fatalf("expected create matrix file event request error, got %v", err)
	}
}

func TestMatrixSender_ExecuteWithRetry_ZeroAttempts(t *testing.T) {
	origMaxAttempts := defaultMaxAttempts
	defer func() { defaultMaxAttempts = origMaxAttempts }()
	defaultMaxAttempts = 0

	sender := NewMatrixSender("https://xi.example.org", "!room:example.org", "token-xi", "Site Xi")
	err := sender.executeWithRetry(context.Background(), func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodGet, "https://target.example.org", http.NoBody)
	})
	if err == nil || !strings.Contains(err.Error(), "matrix request failed with unknown error") {
		t.Fatalf("expected unknown error on zero attempts, got %v", err)
	}
}
