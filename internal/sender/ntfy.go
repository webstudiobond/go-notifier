package sender

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// NtfySender dispatches notifications to ntfy instances.
type NtfySender struct {
	client   *http.Client
	baseURL  string
	topic    string
	token    string
	siteName string
}

// NewNtfySender creates an initialized NtfySender driver.
func NewNtfySender(baseURL, topic, token, siteName string) *NtfySender {
	return &NtfySender{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL:  strings.TrimRight(baseURL, "/"),
		topic:    topic,
		token:    token,
		siteName: siteName,
	}
}

// Name returns the driver identifier.
func (s *NtfySender) Name() string {
	return "ntfy"
}

// Send delivers notification messages and optional attachments to ntfy.
func (s *NtfySender) Send(ctx context.Context, msg *Message, sendAttachments bool) error {
	content := msg.BodyText
	if content == "" {
		content = msg.BodyHTML
	}
	title := fmt.Sprintf("[%s] %s", s.siteName, msg.Subject)

	if err := s.sendTextMessage(ctx, title, content); err != nil {
		return err
	}

	if sendAttachments && len(msg.Attachments) > 0 {
		for _, att := range msg.Attachments {
			if err := s.sendFile(ctx, title, att); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *NtfySender) sendTextMessage(ctx context.Context, title, content string) error {
	endpoint := fmt.Sprintf("%s/%s", s.baseURL, s.topic)

	return s.executeWithRetry(ctx, func() (*http.Request, error) {
		req, err := newHTTPRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(content))
		if err != nil {
			return nil, fmt.Errorf("create ntfy request: %w", err)
		}
		req.Header.Set("Title", title)
		if s.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.token)
		}
		return req, nil
	})
}

func (s *NtfySender) sendFile(ctx context.Context, title string, att Attachment) error {
	data, err := base64.StdEncoding.DecodeString(att.ContentBase64)
	if err != nil {
		return fmt.Errorf("decode ntfy attachment %s: %w", att.Filename, err)
	}

	endpoint := fmt.Sprintf("%s/%s", s.baseURL, s.topic)

	return s.executeWithRetry(ctx, func() (*http.Request, error) {
		req, err := newHTTPRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("create ntfy file request: %w", err)
		}
		req.Header.Set("Title", fmt.Sprintf("%s: %s", title, att.Filename))
		req.Header.Set("Filename", att.Filename)
		if s.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.token)
		}
		return req, nil
	})
}

func (s *NtfySender) executeWithRetry(ctx context.Context, reqFactory func() (*http.Request, error)) error {
	var lastErr error
	for attempt := range defaultMaxAttempts {
		req, err := reqFactory()
		if err != nil {
			return err
		}

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("ntfy request failed: %w", err)
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			case <-time.After(retryDelay(attempt)):
				continue
			}
		}

		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte("unable to read response")
		}

		closeErr := resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if closeErr != nil {
				return fmt.Errorf("close ntfy response: %w", closeErr)
			}
			return nil
		}

		if closeErr != nil {
			lastErr = fmt.Errorf("ntfy returned status %d: %s (close: %w)", resp.StatusCode, string(body), closeErr)
		} else {
			lastErr = fmt.Errorf("ntfy returned status %d: %s", resp.StatusCode, string(body))
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		case <-time.After(retryDelay(attempt)):
		}
	}

	if lastErr == nil {
		return errors.New("ntfy request failed with unknown error")
	}
	return lastErr
}
