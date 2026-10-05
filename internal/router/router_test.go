package router

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"slices"
	"sync"
	"testing"

	"github.com/webstudiobond/go-notifier/internal/config"
	"github.com/webstudiobond/go-notifier/internal/sender"
)

type mockSender struct {
	sendFunc func(ctx context.Context, msg *sender.Message, sendAttachments bool) error
	name     string
}

func (m *mockSender) Name() string {
	return m.name
}

func (m *mockSender) Send(ctx context.Context, msg *sender.Message, sendAttachments bool) error {
	if m.sendFunc != nil {
		return m.sendFunc(ctx, msg, sendAttachments)
	}
	return nil
}

func TestRouter_IsAdminNotification(t *testing.T) {
	tests := []struct {
		name        string
		adminEmails []string
		recipients  []string
		expected    bool
	}{
		{
			name:        "empty admin list",
			adminEmails: nil,
			recipients:  []string{"root@alpha.example"},
			expected:    false,
		},
		{
			name:        "matching admin recipient with whitespace and casing",
			adminEmails: []string{"admin@beta.example"},
			recipients:  []string{"  ADMIN@BETA.EXAMPLE "},
			expected:    true,
		},
		{
			name:        "non admin customer recipient",
			adminEmails: []string{"security@gamma.example"},
			recipients:  []string{"buyer@customer.example"},
			expected:    false,
		},
		{
			name:        "mixed recipients with admin present",
			adminEmails: []string{"alerts@delta.example"},
			recipients:  []string{"user@client.example", "alerts@delta.example"},
			expected:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				AdminEmails: tt.adminEmails,
			}
			r := NewRouter(cfg, nil)
			msg := &sender.Message{To: tt.recipients}
			got := r.IsAdminNotification(msg)
			if got != tt.expected {
				t.Errorf("IsAdminNotification() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestRouter_ResolveTargets(t *testing.T) {
	securityRegex := regexp.MustCompile(`(?i)(security|attack)`)
	backupRegex := regexp.MustCompile(`(?i)backup`)

	defaultChannels := []string{"ch-primary", "ch-secondary"}
	cfg := &config.Config{
		AdminEmails: []string{
			"admin@one.example",
			"admin@two.example",
			"admin@three.example",
		},
		DefaultChannels:     defaultChannels,
		SMTPEnabled:         true,
		AdminFilterRequired: true,
		Routes: []config.RouteRule{
			{
				MatchRegex:      securityRegex,
				Targets:         []string{"ch-urgent", "ch-chat"},
				SendAttachments: true,
			},
			{
				MatchRegex:      backupRegex,
				Targets:         nil,
				SendAttachments: true,
			},
		},
	}

	tests := []struct {
		name                string
		to                  []string
		subject             string
		expectedTargets     []string
		expectedAttachments bool
	}{
		{
			name:                "customer recipient routes strictly to smtp with attachments",
			to:                  []string{"customer@store.example"},
			subject:             "Security alert",
			expectedTargets:     []string{"smtp"},
			expectedAttachments: true,
		},
		{
			name:                "admin recipient matching custom route with targets",
			to:                  []string{"admin@one.example"},
			subject:             "Attack blocked on login",
			expectedTargets:     []string{"ch-urgent", "ch-chat"},
			expectedAttachments: true,
		},
		{
			name:                "admin recipient matching route with fallback to default channels",
			to:                  []string{"admin@two.example"},
			subject:             "Daily Backup Completed",
			expectedTargets:     defaultChannels,
			expectedAttachments: true,
		},
		{
			name:                "admin recipient with unmatched subject uses default channels without attachments",
			to:                  []string{"admin@three.example"},
			subject:             "New user registered",
			expectedTargets:     defaultChannels,
			expectedAttachments: false,
		},
	}

	r := NewRouter(cfg, nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &sender.Message{
				To:      tt.to,
				Subject: tt.subject,
			}
			targets, sendAttachments := r.ResolveTargets(msg)
			if len(targets) != len(tt.expectedTargets) {
				t.Fatalf("expected targets %v, got %v", tt.expectedTargets, targets)
			}
			for i := range targets {
				if targets[i] != tt.expectedTargets[i] {
					t.Errorf("target[%d] = %s, want %s", i, targets[i], tt.expectedTargets[i])
				}
			}
			if sendAttachments != tt.expectedAttachments {
				t.Errorf("sendAttachments = %v, want %v", sendAttachments, tt.expectedAttachments)
			}
		})
	}
}

func TestRouter_Dispatch(t *testing.T) {
	tests := []struct {
		name            string
		adminEmail      string
		subject         string
		channels        []string
		failingChannels []string
		omitChannels    []string
		expectError     bool
	}{
		{
			name:        "successful concurrent dispatch to default channels",
			adminEmail:  "admin@success.example",
			subject:     "Standard Admin Alert",
			channels:    []string{"alpha", "beta"},
			expectError: false,
		},
		{
			name:            "dispatch collects sender errors",
			adminEmail:      "admin@err.example",
			subject:         "Trigger Failure",
			channels:        []string{"omega"},
			failingChannels: []string{"omega"},
			expectError:     true,
		},
		{
			name:        "dispatch fails when no targets resolved",
			adminEmail:  "admin@empty.example",
			subject:     "No Target Delivery",
			channels:    nil,
			expectError: true,
		},
		{
			name:         "dispatch skips channel missing from senders map",
			adminEmail:   "admin@unregistered.example",
			subject:      "Missing Sender Channel",
			channels:     []string{"present", "missing"},
			omitChannels: []string{"missing"},
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			senders := make(map[string]sender.Sender, len(tt.channels))
			for _, ch := range tt.channels {
				if slices.Contains(tt.omitChannels, ch) {
					continue
				}
				var sendErr error
				if slices.Contains(tt.failingChannels, ch) {
					sendErr = errors.New("dispatch network failure")
				}
				senders[ch] = &mockSender{
					name: ch,
					sendFunc: func(_ context.Context, _ *sender.Message, _ bool) error {
						return sendErr
					},
				}
			}

			cfg := &config.Config{
				AdminEmails:     []string{tt.adminEmail},
				DefaultChannels: tt.channels,
			}
			r := NewRouter(cfg, senders)
			msg := &sender.Message{
				To:      []string{tt.adminEmail},
				Subject: tt.subject,
			}

			err := r.Dispatch(context.Background(), msg)
			if (err != nil) != tt.expectError {
				t.Fatalf("Dispatch() error = %v, expectError = %v", err, tt.expectError)
			}
		})
	}
}

func TestRouter_DispatchConcurrency(t *testing.T) {
	var mu sync.Mutex
	delivered := make(map[string]bool)

	channelNames := []string{"stream-left", "stream-right"}
	senders := make(map[string]sender.Sender, len(channelNames))
	for _, name := range channelNames {
		chName := name
		senders[chName] = &mockSender{
			name: chName,
			sendFunc: func(_ context.Context, _ *sender.Message, _ bool) error {
				mu.Lock()
				delivered[chName] = true
				mu.Unlock()
				return nil
			},
		}
	}

	cfg := &config.Config{
		AdminEmails:     []string{"admin@concurrent.example"},
		DefaultChannels: channelNames,
	}

	r := NewRouter(cfg, senders)
	msg := &sender.Message{
		To:      []string{"admin@concurrent.example"},
		Subject: "Concurrent Alert",
	}

	if err := r.Dispatch(context.Background(), msg); err != nil {
		t.Fatalf("concurrent dispatch error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, name := range channelNames {
		if !delivered[name] {
			t.Errorf("expected channel %s to be delivered", name)
		}
	}
}

func TestRouter_AdminFilterDisabled(t *testing.T) {
	alertRegex := regexp.MustCompile(`(?i)alert`)
	cfg := &config.Config{
		AdminEmails: []string{
			"admin@filter.example",
		},
		DefaultChannels:     []string{"default-channel"},
		SMTPEnabled:         true,
		AdminFilterRequired: false,
		Routes: []config.RouteRule{
			{
				MatchRegex: alertRegex,
				Targets:    []string{"alert-channel"},
			},
		},
	}

	r := NewRouter(cfg, nil)
	msg := &sender.Message{
		To:      []string{"nonadmin@customer.example"},
		Subject: "Critical alert occurred",
	}

	targets, sendAttachments := r.ResolveTargets(msg)
	if len(targets) != 1 || targets[0] != "alert-channel" {
		t.Fatalf("expected targets [alert-channel], got %v", targets)
	}
	if sendAttachments {
		t.Errorf("expected sendAttachments to be false")
	}
}

func TestRouter_Dispatch_PureMessengers(t *testing.T) {
	var deliveredLeft, deliveredRight bool
	channelNames := []string{"stream-left", "stream-right"}
	senders := map[string]sender.Sender{
		channelNames[0]: &mockSender{
			name: channelNames[0],
			sendFunc: func(_ context.Context, _ *sender.Message, _ bool) error {
				deliveredLeft = true
				return nil
			},
		},
		channelNames[1]: &mockSender{
			name: channelNames[1],
			sendFunc: func(_ context.Context, _ *sender.Message, _ bool) error {
				deliveredRight = true
				return nil
			},
		},
	}

	cfg := &config.Config{
		SMTPEnabled:         false,
		AdminFilterRequired: false,
		DefaultChannels:     channelNames,
	}

	r := NewRouter(cfg, senders)
	msg := &sender.Message{
		Subject:  "Server disk warning",
		BodyText: "Disk space is low",
	}

	if err := r.Dispatch(context.Background(), msg); err != nil {
		t.Fatalf("Dispatch error: %v", err)
	}

	if !deliveredLeft || !deliveredRight {
		t.Errorf("expected both channels delivered, got left=%v right=%v", deliveredLeft, deliveredRight)
	}
}

func createSMTPSenderMap(extraCh string, receivedMsgs map[string]*sender.Message, mu *sync.Mutex) (channels []string, senders map[string]sender.Sender) {
	channels = []string{"smtp"}
	if extraCh != "" {
		channels = append(channels, extraCh)
	}

	senders = make(map[string]sender.Sender, len(channels))
	for _, ch := range channels {
		chName := ch
		senders[chName] = &mockSender{
			name: chName,
			sendFunc: func(_ context.Context, m *sender.Message, _ bool) error {
				mu.Lock()
				receivedMsgs[chName] = m
				mu.Unlock()
				return nil
			},
		}
	}
	return channels, senders
}

func TestRouter_Dispatch_SMTPScenarios(t *testing.T) {
	tests := []struct {
		expectedErr error
		name        string
		extraCh     string
		skipFirst   bool
	}{
		{
			name:        "smtp_only_requires_to_recipient",
			expectedErr: ErrMissingSMTPRecipients,
		},
		{
			name:      "multi_channel_skips_smtp_when_to_empty",
			extraCh:   "stream-alert",
			skipFirst: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			delivered := make(map[string]*sender.Message)

			channels, senders := createSMTPSenderMap(tt.extraCh, delivered, &mu)

			cfg := &config.Config{
				DefaultChannels:     channels,
				SMTPEnabled:         true,
				AdminFilterRequired: false,
			}

			r := NewRouter(cfg, senders)
			msg := &sender.Message{
				Subject:  "Broadcast without recipients",
				BodyText: "Broadcasting to messengers",
			}

			err := r.Dispatch(context.Background(), msg)
			if tt.expectedErr != nil {
				if !errors.Is(err, tt.expectedErr) {
					t.Fatalf("expected error %v, got %v", tt.expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected Dispatch error: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()
			for _, ch := range channels {
				if tt.skipFirst && ch == channels[0] {
					if delivered[ch] != nil {
						t.Errorf("expected channel %s to be skipped", ch)
					}
				} else if delivered[ch] == nil {
					t.Errorf("expected channel %s to be delivered", ch)
				}
			}
		})
	}
}

func TestRouter_Dispatch_HTMLHandling(t *testing.T) {
	tests := []struct {
		name                 string
		messengerCh          string
		initialText          string
		initialHTML          string
		wantBodyText         string
		recipient            string
		initialAttachments   []sender.Attachment
		routeAttachments     bool
		wantMessengerHTMLAtt bool
		wantSMTPHTMLAtt      bool
	}{
		{
			name:                 "html_only_generates_plain_text_and_attaches_to_messenger_when_enabled",
			messengerCh:          "stream-telegram",
			recipient:            "admin@alpha.example",
			routeAttachments:     true,
			initialHTML:          "<h2>Security Notice</h2><p>Login from IP</p>",
			wantBodyText:         "Security Notice\n\nLogin from IP",
			wantMessengerHTMLAtt: true,
			wantSMTPHTMLAtt:      false,
		},
		{
			name:                 "explicit_text_is_preserved_and_not_overwritten",
			messengerCh:          "stream-matrix",
			recipient:            "admin@beta.example",
			routeAttachments:     true,
			initialText:          "Explicit verbatim text",
			initialHTML:          "<p>HTML content</p>",
			wantBodyText:         "Explicit verbatim text",
			wantMessengerHTMLAtt: true,
			wantSMTPHTMLAtt:      false,
		},
		{
			name:                 "attachments_disabled_does_not_attach_html_to_messenger",
			messengerCh:          "stream-ntfy",
			recipient:            "admin@gamma.example",
			routeAttachments:     false,
			initialHTML:          "<p>Message body</p>",
			wantBodyText:         "Message body",
			wantMessengerHTMLAtt: false,
			wantSMTPHTMLAtt:      false,
		},
		{
			name:             "existing_html_filename_attachment_prevents_duplicate",
			messengerCh:      "stream-chat",
			recipient:        "admin@delta.example",
			routeAttachments: true,
			initialText:      "Initial unformatted body",
			initialHTML:      "<p>Body</p>",
			initialAttachments: []sender.Attachment{
				{
					Filename:      "message.html",
					MIMEType:      "text/html",
					ContentBase64: base64.StdEncoding.EncodeToString([]byte("<p>Body</p>")),
				},
			},
			wantBodyText:         "Initial unformatted body",
			wantMessengerHTMLAtt: true,
			wantSMTPHTMLAtt:      true,
		},
		{
			name:             "existing_html_mimetype_attachment_prevents_duplicate",
			messengerCh:      "stream-push",
			recipient:        "admin@epsilon.example",
			routeAttachments: true,
			initialText:      "Another pre-existing text",
			initialHTML:      "<p>Body</p>",
			initialAttachments: []sender.Attachment{
				{
					Filename:      "report.doc",
					MIMEType:      "text/html",
					ContentBase64: base64.StdEncoding.EncodeToString([]byte("<p>Body</p>")),
				},
			},
			wantBodyText:         "Another pre-existing text",
			wantMessengerHTMLAtt: false,
			wantSMTPHTMLAtt:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			receivedMsgs := make(map[string]*sender.Message)

			channels, senders := createSMTPSenderMap(tt.messengerCh, receivedMsgs, &mu)

			cfg := &config.Config{
				DefaultChannels:     channels,
				SMTPEnabled:         true,
				AdminFilterRequired: false,
				Routes: []config.RouteRule{
					{
						MatchRegex:      regexp.MustCompile(`.*`),
						Targets:         channels,
						SendAttachments: tt.routeAttachments,
					},
				},
			}

			r := NewRouter(cfg, senders)
			msg := &sender.Message{
				To:          []string{tt.recipient},
				Subject:     "Broadcast alert",
				BodyText:    tt.initialText,
				BodyHTML:    tt.initialHTML,
				Attachments: tt.initialAttachments,
			}

			if err := r.Dispatch(context.Background(), msg); err != nil {
				t.Fatalf("unexpected Dispatch error: %v", err)
			}

			if msg.BodyText != tt.wantBodyText {
				t.Errorf("msg.BodyText = %q, want %q", msg.BodyText, tt.wantBodyText)
			}

			hasHTMLAtt := func(atts []sender.Attachment) bool {
				for _, a := range atts {
					if a.Filename == "message.html" {
						return true
					}
				}
				return false
			}

			mu.Lock()
			smtpPayload := receivedMsgs[channels[0]]
			messengerPayload := receivedMsgs[tt.messengerCh]
			mu.Unlock()

			if smtpPayload == nil || messengerPayload == nil {
				t.Fatalf("expected both channels to receive payload")
			}

			if gotSMTPAtt := hasHTMLAtt(smtpPayload.Attachments); gotSMTPAtt != tt.wantSMTPHTMLAtt {
				t.Errorf("SMTP hasHTMLAtt = %v, want %v", gotSMTPAtt, tt.wantSMTPHTMLAtt)
			}
			if gotMessengerAtt := hasHTMLAtt(messengerPayload.Attachments); gotMessengerAtt != tt.wantMessengerHTMLAtt {
				t.Errorf("Messenger hasHTMLAtt = %v, want %v", gotMessengerAtt, tt.wantMessengerHTMLAtt)
			}
			if len(tt.initialAttachments) > 0 && len(messengerPayload.Attachments) != 1 {
				t.Errorf("Messenger attachments count = %d, want 1", len(messengerPayload.Attachments))
			}
		})
	}
}
