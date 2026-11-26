package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/andaru-project/andaru/internal/delivery"
)

// sendWebhook posts the notification's event as JSON, as the bus carries it. Any 2xx answer is acceptance.
func sendWebhook(ctx context.Context, client *http.Client, job delivery.Job) error {
	t := job.Webhook
	if t == nil {
		return permanent(errors.New("a webhook job without a target"))
	}
	u, err := url.Parse(t.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return permanent(errors.New("the webhook URL is not an http or https URL"))
	}
	if !json.Valid(job.Event) {
		return permanent(errors.New("a webhook job without the notification's event"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(job.Event))
	if err != nil {
		return permanent(errors.New("the webhook request cannot be built")) // its error would show the URL
	}
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	return post(client, req, "the webhook", nil)
}

// post sends req and judges the answer. A 2xx is acceptance, when accepted, given the answer's first 64 KiB, says so;
// 408, 429 and 5xx are retried; anything else, a redirect included (it is not followed), comes back the same on a
// retry. The body comes from the network: no more than a little is read, and none of it is shown.
// credential refuses, for good, a credential with a control character other than a tab: HTTP refuses it, and a
// service answers it no better on a retry.
func credential(service, v string) error {
	if strings.ContainsFunc(v, func(c rune) bool { return c < 0x20 && c != '\t' || c == 0x7f }) {
		return permanent(fmt.Errorf("%s: a credential holds a control character", service))
	}
	return nil
}

func post(client *http.Client, req *http.Request, service string, accepted func(body []byte) bool) error {
	// A header value with a control character, as a pasted token's newline, fails every attempt alike.
	for _, values := range req.Header {
		for _, v := range values {
			if err := credential(service, v); err != nil {
				return err
			}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return redact(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode/100 == 2 && accepted != nil && readErr != nil:
		// The answer was cut off before it said whether the message was taken: try again, at the risk of a repeat.
		return fmt.Errorf("%s answer cut off: %w", service, readErr)
	case resp.StatusCode/100 == 2 && (accepted == nil || accepted(body)):
		return nil
	case resp.StatusCode/100 == 2:
		return permanent(fmt.Errorf("%s did not accept the message", service))
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode/100 == 5:
		return fmt.Errorf("%s answered %s", service, resp.Status)
	}
	return permanent(fmt.Errorf("%s answered %s", service, resp.Status))
}
