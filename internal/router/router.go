// Package router manages privacy filtering and multi-channel message dispatching.
package router

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/webstudiobond/go-notifier/internal/config"
	"github.com/webstudiobond/go-notifier/internal/sender"
)

// ErrMissingSMTPRecipients indicates that SMTP delivery was requested without any recipient addresses.
var ErrMissingSMTPRecipients = errors.New("cannot deliver mail: recipient list (to) is required for SMTP delivery")

// Router handles recipient segregation, route matching, and concurrent channel dispatch.
type Router struct {
	adminEmails         map[string]struct{}
	senders             map[string]sender.Sender
	routes              []config.RouteRule
	defaultChannels     []string
	adminFilterRequired bool
	smtpEnabled         bool
}

// NewRouter constructs an initialized Router instance.
func NewRouter(cfg *config.Config, senders map[string]sender.Sender) *Router {
	emailMap := make(map[string]struct{}, len(cfg.AdminEmails))
	for _, em := range cfg.AdminEmails {
		clean := strings.ToLower(strings.TrimSpace(em))
		if clean != "" {
			emailMap[clean] = struct{}{}
		}
	}

	return &Router{
		senders:             senders,
		adminEmails:         emailMap,
		routes:              cfg.Routes,
		defaultChannels:     cfg.DefaultChannels,
		adminFilterRequired: cfg.AdminFilterRequired,
		smtpEnabled:         cfg.SMTPEnabled,
	}
}

// IsAdminNotification reports whether any recipient in msg matches the explicit admin email list.
func (r *Router) IsAdminNotification(msg *sender.Message) bool {
	if len(r.adminEmails) == 0 {
		return false
	}

	for _, recipient := range msg.To {
		clean := strings.ToLower(strings.TrimSpace(recipient))
		if _, exists := r.adminEmails[clean]; exists {
			return true
		}
	}
	return false
}

// ResolveTargets determines target channels and attachment delivery status for a message.
func (r *Router) ResolveTargets(msg *sender.Message) (targets []string, sendAttachments bool) {
	if r.smtpEnabled && r.adminFilterRequired && !r.IsAdminNotification(msg) {
		return []string{"smtp"}, true
	}

	for _, route := range r.routes {
		if route.MatchRegex != nil && route.MatchRegex.MatchString(msg.Subject) {
			if len(route.Targets) > 0 {
				return route.Targets, route.SendAttachments
			}
			return r.defaultChannels, route.SendAttachments
		}
	}

	return r.defaultChannels, false
}

// Dispatch concurrently routes and delivers a message to all resolved channels.
func (r *Router) Dispatch(ctx context.Context, msg *sender.Message) error {
	targets, sendAttachments := r.ResolveTargets(msg)
	if len(targets) == 0 {
		return errors.New("no active channels resolved for delivery")
	}

	if slices.Contains(targets, "smtp") && len(msg.To) == 0 {
		if len(targets) == 1 {
			return ErrMissingSMTPRecipients
		}
		filtered := make([]string, 0, len(targets)-1)
		for _, t := range targets {
			if t != "smtp" {
				filtered = append(filtered, t)
			}
		}
		targets = filtered
	}

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var errs []error

	for _, targetName := range targets {
		s, exists := r.senders[targetName]
		if !exists {
			continue
		}

		wg.Add(1)
		go func(drv sender.Sender) {
			defer wg.Done()
			if err := drv.Send(ctx, msg, sendAttachments); err != nil {
				errMu.Lock()
				errs = append(errs, fmt.Errorf("sender %s error: %w", drv.Name(), err))
				errMu.Unlock()
			}
		}(s)
	}

	wg.Wait()

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
