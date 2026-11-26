package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/andaru-project/andaru/internal/delivery"
	"github.com/andaru-project/andaru/internal/notify"
)

// The services' API endpoints, which a user cannot change; tests point them at their own servers.
var (
	pushoverAPI = "https://api.pushover.net/1/messages.json"
	telegramAPI = "https://api.telegram.org"
)

// sendNtfy publishes the message to the ntfy topic as JSON, which carries any text a header could not: the subject as
// the title, a priority by severity and the full link, when there is one, as the click action.
func sendNtfy(ctx context.Context, client *http.Client, job delivery.Job) error {
	t := job.Ntfy
	if t == nil {
		return permanent(errors.New("an ntfy job without a target"))
	}
	u, err := url.Parse(t.Server)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || t.Topic == "" {
		return permanent(errors.New("the ntfy server is not an http or https URL, or the topic is empty"))
	}
	m := job.Message
	message := map[string]any{"topic": t.Topic, "title": m.Subject, "message": text(m), "priority": ntfyPriority(m.Severity),
		"tags": []string{"andaru"}}
	if l := link(job); l != "" {
		message["click"] = l
	}
	body, err := json.Marshal(message)
	if err != nil {
		return permanent(err)
	}
	// ntfy takes a JSON message at its root.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(t.Server, "/")+"/", bytes.NewReader(body))
	if err != nil {
		return permanent(errors.New("the ntfy request cannot be built"))
	}
	req.Header.Set("Content-Type", "application/json")
	if t.Token != "" {
		req.Header.Set("Authorization", "Bearer "+t.Token)
	}
	return post(client, req, "ntfy", nil)
}

// sendPushover sends the message through Pushover's API, which answers {"status":1} once it takes it.
func sendPushover(ctx context.Context, client *http.Client, job delivery.Job) error {
	t := job.Pushover
	if t == nil || t.Token == "" || t.User == "" {
		return permanent(errors.New("a Pushover job without its token and user key"))
	}
	m := job.Message
	// Pushover's limits are 250 characters for a title, 1024 for a message and 512 for a URL.
	form := url.Values{"token": {t.Token}, "user": {t.User}, "title": {cut(m.Subject, 250)}, "message": {cut(text(m), 1024)},
		"priority": {strconv.Itoa(pushoverPriority(m.Severity))}}
	if l := link(job); l != "" && len(l) <= 512 {
		form.Set("url", l)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushoverAPI, strings.NewReader(form.Encode()))
	if err != nil {
		return permanent(errors.New("the Pushover request cannot be built"))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return post(client, req, "Pushover", func(body []byte) bool {
		var answer struct{ Status int }
		return json.Unmarshal(body, &answer) == nil && answer.Status == 1
	})
}

// sendTelegram sends the message to the chat through the bot's sendMessage, which answers {"ok":true} once it takes it.
// The bot's token is in the request's path: no error may show the URL.
func sendTelegram(ctx context.Context, client *http.Client, job delivery.Job) error {
	t := job.Telegram
	if t == nil || t.Token == "" || t.ChatID == "" {
		return permanent(errors.New("a Telegram job without its bot token and chat ID"))
	}
	l := link(job)
	if l == "" {
		l = job.Message.Link // a path in the web UI still says where to look
	}
	body, err := json.Marshal(map[string]any{"chat_id": t.ChatID, "text": telegramText(job.Message, l), "parse_mode": "HTML",
		"disable_web_page_preview": true})
	if err != nil {
		return permanent(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramAPI+"/bot"+url.PathEscape(t.Token)+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return permanent(errors.New("the Telegram request cannot be built"))
	}
	req.Header.Set("Content-Type", "application/json")
	return post(client, req, "Telegram", func(body []byte) bool {
		var answer struct{ OK bool }
		return json.Unmarshal(body, &answer) == nil && answer.OK
	})
}

// ntfyPriority is ntfy's priority, from 1 to 5, for a severity: info the default 3, warning 4, critical 5.
func ntfyPriority(severity string) int {
	switch severity {
	case notify.Critical:
		return 5
	case notify.Warning:
		return 4
	}
	return 3
}

// pushoverPriority is 1, high, for a critical notification and 0 otherwise; never 2, emergency, which Pushover repeats
// until it is acknowledged.
func pushoverPriority(severity string) int {
	if severity == notify.Critical {
		return 1
	}
	return 0
}

// telegramText is the message as Telegram HTML: the subject in bold, then the detail and the link. The detail is cut
// to 2048 bytes, which keeps the whole within Telegram's 4096 characters.
func telegramText(m delivery.Message, link string) string {
	s := "<b>" + html.EscapeString(m.Subject) + "</b>"
	for _, part := range []string{cut(m.Detail, 2048), link} {
		if part != "" {
			s += "\n\n" + html.EscapeString(part)
		}
	}
	return s
}

// text is the message's body: its detail, or its subject when it has none.
func text(m delivery.Message) string {
	if m.Detail != "" {
		return m.Detail
	}
	return m.Subject
}

// link is the message's link as a full URL, or empty when it has none or the web UI's origin is not known.
func link(job delivery.Job) string {
	if job.Origin == "" || job.Message.Link == "" {
		return ""
	}
	return strings.TrimSuffix(job.Origin, "/") + job.Message.Link
}

// cut cuts s to at most n bytes, dropping a character the cut would split.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
