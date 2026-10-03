package sender

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

type tgStepWriter struct {
	failOnStep  int
	currentStep int
}

func (s *tgStepWriter) Write(p []byte) (int, error) {
	s.currentStep++
	if s.currentStep == s.failOnStep {
		return 0, errors.New("simulated multipart write failure")
	}
	return len(p), nil
}

func mockFailingTelegramJSONMarshal(_ any) ([]byte, error) {
	return nil, errors.New("simulated json marshal failure")
}

func mockFailingTelegramHTTPRequestWithContext(_ context.Context, _, _ string, _ io.Reader) (*http.Request, error) {
	return nil, errors.New("simulated http request creation failure")
}

func mockStep1MultipartWriter(_ io.Writer) *multipart.Writer {
	return multipart.NewWriter(&tgStepWriter{failOnStep: 1})
}

func mockStep3MultipartWriter(_ io.Writer) *multipart.Writer {
	return multipart.NewWriter(&tgStepWriter{failOnStep: 3})
}

func mockStep4MultipartWriter(_ io.Writer) *multipart.Writer {
	return multipart.NewWriter(&tgStepWriter{failOnStep: 4})
}

func mockStep5MultipartWriter(_ io.Writer) *multipart.Writer {
	return multipart.NewWriter(&tgStepWriter{failOnStep: 5})
}

func TestTelegramSender_Name(t *testing.T) {
	sender := NewTelegramSender("123456:ABCdef", "-100123456", "Site Alpha")
	if got := sender.Name(); got != "telegram" {
		t.Fatalf("expected Name %q, got %q", "telegram", got)
	}
}

func TestTelegramSender_FormatMessage(t *testing.T) {
	tests := []struct {
		name        string
		subject     string
		bodyText    string
		bodyHTML    string
		wantContain []string
	}{
		{
			name:        "body_text_preferred",
			subject:     "Subject Text",
			bodyText:    "Body Text Plain",
			bodyHTML:    "<p>Body HTML</p>",
			wantContain: []string{"*Subject:* Subject Text", "Body Text Plain"},
		},
		{
			name:        "body_html_fallback",
			subject:     "Subject HTML",
			bodyText:    "",
			bodyHTML:    "<p>Body HTML Fallback</p>",
			wantContain: []string{"*Subject:* Subject HTML", "<p>Body HTML Fallback</p>"},
		},
		{
			name:        "truncation_over_3500_characters",
			subject:     "Long Subject",
			bodyText:    strings.Repeat("a", 4000),
			wantContain: []string{"\n...(truncated)"},
		},
	}

	sender := NewTelegramSender("123456:ABCdef", "-100123456", "Format Site")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &Message{
				Subject:  tt.subject,
				BodyText: tt.bodyText,
				BodyHTML: tt.bodyHTML,
			}
			formatted := sender.formatMessage(msg)
			for _, exp := range tt.wantContain {
				if !strings.Contains(formatted, exp) {
					t.Fatalf("expected formatted message to contain %q, got:\n%s", exp, formatted)
				}
			}
		})
	}
}

func TestTelegramSender_Send_Branches(t *testing.T) {
	tests := []struct {
		mockTransport   roundTripFunc
		name            string
		wantErr         string
		attachments     []Attachment
		sendAttachments bool
	}{
		{
			name:            "send_text_only_without_attachments_flag",
			sendAttachments: false,
			attachments: []Attachment{
				{Filename: "doc-noatt.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("content"))},
			},
			mockTransport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/sendMessage") {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
						Header:     make(http.Header),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewReader(nil)),
					Header:     make(http.Header),
				}, nil
			}),
		},
		{
			name:            "send_with_empty_attachments_slice",
			sendAttachments: true,
			attachments:     nil,
			mockTransport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
					Header:     make(http.Header),
				}, nil
			}),
		},
		{
			name:            "send_with_document_success",
			sendAttachments: true,
			attachments: []Attachment{
				{Filename: "doc-success.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("content"))},
			},
			mockTransport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
					Header:     make(http.Header),
				}, nil
			}),
		},
		{
			name:            "send_text_failure_aborts",
			sendAttachments: true,
			attachments: []Attachment{
				{Filename: "doc-txtfail.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("content"))},
			},
			mockTransport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"ok":false,"description":"bad request"}`)),
					Header:     make(http.Header),
				}, nil
			}),
			wantErr: "telegram returned status 400",
		},
		{
			name:            "send_document_failure_aborts",
			sendAttachments: true,
			attachments: []Attachment{
				{Filename: "doc-docfail.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("content"))},
			},
			mockTransport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/sendMessage") {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
						Header:     make(http.Header),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(strings.NewReader(`{"ok":false}`)),
					Header:     make(http.Header),
				}, nil
			}),
			wantErr: "telegram returned status 500",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := NewTelegramSender("123456:ABCdef", "-100123456", "Site Branches")
			sender.client = &http.Client{Transport: tt.mockTransport}

			msg := &Message{
				Subject:     "Branch Test",
				BodyText:    "Branch Body",
				Attachments: tt.attachments,
			}
			err := sender.Send(context.Background(), msg, tt.sendAttachments)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error in %s: %v", tt.name, err)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
			}
		})
	}
}

func TestTelegramSender_SendTextMessage_Errors(t *testing.T) {
	tests := []struct {
		marshalHook func(any) ([]byte, error)
		reqHook     func(context.Context, string, string, io.Reader) (*http.Request, error)
		name        string
		wantErr     string
	}{
		{
			marshalHook: mockFailingTelegramJSONMarshal,
			name:        "marshal_failure",
			wantErr:     "marshal telegram payload",
		},
		{
			reqHook: mockFailingTelegramHTTPRequestWithContext,
			name:    "request_creation_failure",
			wantErr: "create telegram message request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.marshalHook != nil {
				origMarshal := jsonMarshal
				defer func() { jsonMarshal = origMarshal }()
				jsonMarshal = tt.marshalHook
			}

			if tt.reqHook != nil {
				origReq := newHTTPRequestWithContext
				defer func() { newHTTPRequestWithContext = origReq }()
				newHTTPRequestWithContext = tt.reqHook
			}

			sender := NewTelegramSender("123456:ABCdef", "-100123456", "Site Errors")
			err := sender.sendTextMessage(context.Background(), "Hello test")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestTelegramSender_SendDocument_Errors(t *testing.T) {
	tests := []struct {
		multipartHook func(io.Writer) *multipart.Writer
		reqHook       func(context.Context, string, string, io.Reader) (*http.Request, error)
		name          string
		att           Attachment
		wantErr       string
	}{
		{
			name:    "corrupted_base64_error",
			att:     Attachment{Filename: "bad.txt", ContentBase64: "invalid-base64-%%%"},
			wantErr: "decode telegram attachment",
		},
		{
			multipartHook: mockStep1MultipartWriter,
			name:          "write_chat_id_field_error",
			att:           Attachment{Filename: "file-chat.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("data"))},
			wantErr:       "write telegram chat_id field",
		},
		{
			multipartHook: mockStep3MultipartWriter,
			name:          "create_form_file_error",
			att:           Attachment{Filename: "file-form.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("data"))},
			wantErr:       "create telegram document form file",
		},
		{
			multipartHook: mockStep4MultipartWriter,
			name:          "write_document_data_error",
			att:           Attachment{Filename: "file-data.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("data"))},
			wantErr:       "write telegram document data",
		},
		{
			multipartHook: mockStep5MultipartWriter,
			name:          "close_multipart_writer_error",
			att:           Attachment{Filename: "file-close.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("data"))},
			wantErr:       "close telegram multipart writer",
		},
		{
			reqHook: mockFailingTelegramHTTPRequestWithContext,
			name:    "request_creation_failure",
			att:     Attachment{Filename: "file-req.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("data"))},
			wantErr: "create telegram document request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.multipartHook != nil {
				origMP := newMultipartWriter
				defer func() { newMultipartWriter = origMP }()
				newMultipartWriter = tt.multipartHook
			}

			if tt.reqHook != nil {
				origReq := newHTTPRequestWithContext
				defer func() { newHTTPRequestWithContext = origReq }()
				newHTTPRequestWithContext = tt.reqHook
			}

			sender := NewTelegramSender("123456:ABCdef", "-100123456", "Site Doc Errors")
			err := sender.sendDocument(context.Background(), tt.att)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestTelegramSender_ExecuteWithRetry_TransportErrors(t *testing.T) {
	tests := []struct {
		reqFactory func() (*http.Request, error)
		transport  roundTripFunc
		name       string
		wantErr    string
		cancelCtx  bool
	}{
		{
			reqFactory: func() (*http.Request, error) {
				return nil, errors.New("simulated factory error")
			},
			name:    "reqFactory_error_returns_immediately",
			wantErr: "simulated factory error",
		},
		{
			name: "transport_retry_success",
			transport: func() roundTripFunc {
				var calls int
				return roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						return nil, errors.New("simulated network glitch")
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
						Header:     make(http.Header),
					}, nil
				})
			}(),
		},
		{
			name: "context_cancelled_during_transport_retry",
			transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("network outage")
			}),
			cancelCtx: true,
			wantErr:   "context cancelled during retry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelCtx {
				cancel()
			}

			sender := NewTelegramSender("123456:ABCdef", "-100123456", "Site Transport")
			if tt.transport != nil {
				sender.client = &http.Client{Transport: tt.transport}
			}

			reqFactory := tt.reqFactory
			if reqFactory == nil {
				reqFactory = func() (*http.Request, error) {
					return http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.example.com", bytes.NewReader(nil))
				}
			}

			err := sender.executeWithRetry(ctx, reqFactory)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
			}
		})
	}
}

func TestTelegramSender_ExecuteWithRetry_ResponseErrors(t *testing.T) {
	tests := []struct {
		transport roundTripFunc
		name      string
		wantErr   string
		cancelCtx bool
	}{
		{
			name: "body_read_error",
			transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       &errReader{},
					Header:     make(http.Header),
				}, nil
			}),
			wantErr: "unable to read response",
		},
		{
			name: "close_error_on_status_ok",
			transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       &errCloser{Reader: strings.NewReader(`{"ok":true}`)},
					Header:     make(http.Header),
				}, nil
			}),
			wantErr: "close telegram response",
		},
		{
			name: "close_error_on_status_error",
			transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       &errCloser{Reader: strings.NewReader("bad input")},
					Header:     make(http.Header),
				}, nil
			}),
			wantErr: "(close: close simulation error)",
		},
		{
			name: "status_error_retry_success",
			transport: func() roundTripFunc {
				var calls int
				return roundTripFunc(func(_ *http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						return &http.Response{
							StatusCode: http.StatusBadGateway,
							Body:       io.NopCloser(strings.NewReader("bad gateway")),
							Header:     make(http.Header),
						}, nil
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
						Header:     make(http.Header),
					}, nil
				})
			}(),
		},
		{
			name: "context_cancelled_during_status_retry",
			transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadGateway,
					Body:       io.NopCloser(strings.NewReader("server busy")),
					Header:     make(http.Header),
				}, nil
			}),
			cancelCtx: true,
			wantErr:   "context cancelled during retry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelCtx {
				cancel()
			}

			sender := NewTelegramSender("123456:ABCdef", "-100123456", "Site Resp")
			sender.client = &http.Client{Transport: tt.transport}

			reqFactory := func() (*http.Request, error) {
				return http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.example.com", bytes.NewReader(nil))
			}

			err := sender.executeWithRetry(ctx, reqFactory)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
			}
		})
	}
}

func TestTelegramSender_ExecuteWithRetry_ZeroAttempts(t *testing.T) {
	origMax := defaultMaxAttempts
	defer func() { defaultMaxAttempts = origMax }()
	defaultMaxAttempts = 0

	sender := NewTelegramSender("123456:ABCdef", "-100123456", "Site Zero")
	reqFactory := func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.telegram.example.com", bytes.NewReader(nil))
	}

	err := sender.executeWithRetry(context.Background(), reqFactory)
	if err == nil || !strings.Contains(err.Error(), "telegram request failed with unknown error") {
		t.Fatalf("expected unknown error, got %v", err)
	}
}
