package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/fanuelsen/repull/internal/sanitize"
)

// httpClient is used for all Discord webhook requests.
// A 10s timeout prevents a hung Discord connection from stalling the update loop.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// Notifier sends notifications to Discord via webhook
type Notifier struct {
	webhookURL string
}

// NewDiscordNotifier creates a new Discord notifier.
// Returns nil if webhookURL is empty (disables notifications).
// Returns an error if the URL is not a valid Discord webhook.
func NewDiscordNotifier(webhookURL string) (*Notifier, error) {
	if webhookURL == "" {
		return nil, nil
	}
	if !strings.HasPrefix(webhookURL, "https://discord.com/api/webhooks/") &&
		!strings.HasPrefix(webhookURL, "https://discordapp.com/api/webhooks/") {
		return nil, fmt.Errorf("invalid Discord webhook URL: must start with https://discord.com/api/webhooks/")
	}
	return &Notifier{webhookURL: webhookURL}, nil
}

// webhookMessage is the payload Discord expects for a simple text message.
// AllowedMentions with an empty parse list disables all mentions: without it,
// a container or image name containing "@everyone" (or a role/user mention)
// would trigger a real ping in the channel.
type webhookMessage struct {
	Content         string          `json:"content"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

type allowedMentions struct {
	Parse []string `json:"parse"`
}

// SendUpdate sends a notification about a successful container update.
// The digest strings are included as-is; callers truncate them for display.
// Failures are logged, not returned: a broken webhook should never affect
// the update cycle itself.
func (n *Notifier) SendUpdate(service, image, oldDigest, newDigest string) {
	if n == nil {
		return
	}

	n.send(fmt.Sprintf("✅ Updated %s\nImage: %s\n%s → %s",
		service, image, oldDigest, newDigest))
}

// SendError sends a notification about an update failure.
// Error messages are truncated to avoid leaking sensitive data (e.g. registry
// credentials that may appear in Docker API error strings) to Discord.
// Failures are logged, not returned: a broken webhook should never affect
// the update cycle itself.
func (n *Notifier) SendError(service, errorMsg string) {
	if n == nil {
		return
	}

	const maxLen = 200
	if len(errorMsg) > maxLen {
		errorMsg = errorMsg[:maxLen] + "..."
	}

	n.send(fmt.Sprintf("❌ Failed to update %s\nError: %s", service, errorMsg))
}

// send performs the HTTP POST to the Discord webhook, logging any failure.
// Content is sanitized here at the sink so no caller can forget it — error
// text in particular can echo registry-controlled response bodies.
func (n *Notifier) send(content string) {
	// Marshalling a struct of strings and a string slice cannot fail.
	data, _ := json.Marshal(webhookMessage{
		Content:         sanitize.String(content),
		AllowedMentions: allowedMentions{Parse: []string{}},
	})

	resp, err := httpClient.Post(n.webhookURL, "application/json", bytes.NewBuffer(data))
	if err != nil {
		log.Printf("[WARN] Discord notification failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[WARN] Discord notification failed: webhook returned status %d", resp.StatusCode)
	}
}
