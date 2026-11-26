// Package delivery is the job format between the notifications layer, which publishes a job on Topic(channel) for
// each notification a channel's filter passes, and andaru-outbound, which sends it.
package delivery

import (
	"encoding/json"
	"time"
)

const (
	// Filter is what andaru-outbound's consumer, Consumer, reads.
	Filter   = "delivery/#"
	Consumer = "deliveries"
	// JobType is the type of a job's event.
	JobType = "delivery.job.v1"
	// Source is the source andaru-outbound raises its own notifications as, on notification/delivery.
	Source = "delivery"
)

// Topic is where the jobs for a channel are published.
func Topic(channel string) string { return "delivery/" + channel }

// Kinds of channel.
const (
	Webhook  = "webhook"
	Ntfy     = "ntfy"
	Pushover = "pushover"
	Telegram = "telegram"
	Email    = "email"
)

// Job is one notification for one channel, with what the channel needs to send it, credentials included.
type Job struct {
	// ID is the notification event's ID and the channel's: a job published again keeps it.
	ID      string  `json:"id"`
	Channel string  `json:"channel"`
	Name    string  `json:"name"` // the channel's name, for the operator
	Kind    string  `json:"kind"`
	Message Message `json:"message"`
	// Origin is the web UI's origin, such as https://fw.example, which makes the message's link a full URL; empty
	// when not known, and a channel that needs a full URL then sends no link.
	Origin string `json:"origin,omitempty"`
	// Event is the notification's own event on the bus, which the webhook sends as it is.
	Event json.RawMessage `json:"event"`
	// Webhook is set for Kind Webhook.
	Webhook *WebhookTarget `json:"webhook,omitempty"`
	// Ntfy, Pushover, Telegram and Email are set for their kinds.
	Ntfy     *NtfyTarget     `json:"ntfy,omitempty"`
	Pushover *PushoverTarget `json:"pushover,omitempty"`
	Telegram *TelegramTarget `json:"telegram,omitempty"`
	Email    *EmailTarget    `json:"email,omitempty"`
}

// Message is the notification a job sends.
type Message struct {
	Source   string    `json:"source"`
	Key      string    `json:"key"`
	Severity string    `json:"severity"`
	Subject  string    `json:"subject"`
	Detail   string    `json:"detail,omitempty"`
	Link     string    `json:"link,omitempty"`
	Raised   time.Time `json:"raised"`
}

// WebhookTarget is an HTTP POST of the message's JSON.
type WebhookTarget struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// NtfyTarget is a topic on an ntfy server; Token, when set, is sent as a bearer token.
type NtfyTarget struct {
	Server string `json:"server"`
	Topic  string `json:"topic"`
	Token  string `json:"token,omitempty"`
}

// PushoverTarget is a Pushover application's token and the user key it sends to.
type PushoverTarget struct {
	Token string `json:"token"`
	User  string `json:"user"`
}

// TelegramTarget is a Telegram bot's token and the chat it sends to.
type TelegramTarget struct {
	Token  string `json:"token"`
	ChatID string `json:"chatId"`
}

// EmailTarget is an SMTP server, its credentials when it takes any, and the addresses a message is sent from and to.
// Security is "tls", implicit TLS, or "starttls".
type EmailTarget struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Security string   `json:"security"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"password,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}
