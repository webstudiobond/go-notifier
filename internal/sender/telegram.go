package sender

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

func defaultNewMultipartWriter(w io.Writer) *multipart.Writer {
	return multipart.NewWriter(w)
}

var (
	newMultipartWriter = defaultNewMultipartWriter
)

// TelegramSender dispatches notifications and documents to Telegram.
type TelegramSender struct {
	client   *http.Client
	baseURL  string
	token    string
	chatID   string
	siteName string
}

type tgMessagePayload struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

// NewTelegramSender creates an initialized TelegramSender driver.
func NewTelegramSender(token, chatID, siteName string) *TelegramSender {
	return &TelegramSender{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL:  "https://api.telegram.org",
		token:    token,
		chatID:   chatID,
		siteName: siteName,
	}
}

// Name returns the driver identifier.
func (s *TelegramSender) Name() string {
	return "telegram"
}

// Send formats and delivers text messages and optional documents to Telegram.
func (s *TelegramSender) Send(ctx context.Context, msg *Message, sendAttachments bool) error {
	text := s.formatMessage(msg)
	if err := s.sendTextMessage(ctx, text); err != nil {
		return err
	}

	if sendAttachments && len(msg.Attachments) > 0 {
		for _, att := range msg.Attachments {
			if err := s.sendDocument(ctx, att); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *TelegramSender) formatMessage(msg *Message) string {
	var sb strings.Builder
	sb.WriteString("🔔 *")
	sb.WriteString(s.siteName)
	sb.WriteString("*\n\n")

	sb.WriteString("*Subject:* ")
	sb.WriteString(msg.Subject)
	sb.WriteString("\n\n")

	content := msg.BodyText
	if content == "" {
		content = msg.BodyHTML
	}
	if len(content) > 3500 {
		content = content[:3500] + "\n...(truncated)"
	}
	sb.WriteString(content)
	return sb.String()
}

func (s *TelegramSender) sendTextMessage(ctx context.Context, text string) error {
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", s.baseURL, s.token)
	payload := tgMessagePayload{
		ChatID:    s.chatID,
		Text:      text,
		ParseMode: "Markdown",
	}

	raw, err := jsonMarshal(payload)
	if err != nil {
		return fmt.Errorf("marshal telegram payload: %w", err)
	}

	return s.executeWithRetry(ctx, func() (*http.Request, error) {
		req, err := newHTTPRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("create telegram message request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

func (s *TelegramSender) sendDocument(ctx context.Context, att Attachment) error {
	data, err := base64.StdEncoding.DecodeString(att.ContentBase64)
	if err != nil {
		return fmt.Errorf("decode telegram attachment %s: %w", att.Filename, err)
	}

	var buf bytes.Buffer
	w := newMultipartWriter(&buf)

	if err = w.WriteField("chat_id", s.chatID); err != nil {
		return fmt.Errorf("write telegram chat_id field: %w", err)
	}

	part, err := w.CreateFormFile("document", att.Filename)
	if err != nil {
		return fmt.Errorf("create telegram document form file: %w", err)
	}

	if _, err = part.Write(data); err != nil {
		return fmt.Errorf("write telegram document data: %w", err)
	}

	if err = w.Close(); err != nil {
		return fmt.Errorf("close telegram multipart writer: %w", err)
	}

	endpoint := fmt.Sprintf("%s/bot%s/sendDocument", s.baseURL, s.token)
	contentType := w.FormDataContentType()

	return s.executeWithRetry(ctx, func() (*http.Request, error) {
		req, err := newHTTPRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf.Bytes()))
		if err != nil {
			return nil, fmt.Errorf("create telegram document request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)
		return req, nil
	})
}

func (s *TelegramSender) executeWithRetry(ctx context.Context, reqFactory func() (*http.Request, error)) error {
	var lastErr error
	for attempt := range defaultMaxAttempts {
		req, err := reqFactory()
		if err != nil {
			return err
		}

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("telegram request failed: %w", err)
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
				return fmt.Errorf("close telegram response: %w", closeErr)
			}
			return nil
		}

		if closeErr != nil {
			lastErr = fmt.Errorf("telegram returned status %d: %s (close: %w)", resp.StatusCode, string(body), closeErr)
		} else {
			lastErr = fmt.Errorf("telegram returned status %d: %s", resp.StatusCode, string(body))
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		case <-time.After(retryDelay(attempt)):
		}
	}

	if lastErr == nil {
		return errors.New("telegram request failed with unknown error")
	}
	return lastErr
}
