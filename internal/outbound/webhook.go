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

	"github.com/andaru-project/andaru/internal/delivery"
)

// sendWebhook posts the notification's event as JSON, as the bus carries it. Any 2xx answer is acceptance; the body is not read beyond a little, since it
// comes from the network and nothing is done with it.
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
	resp, err := client.Do(req)
	if err != nil {
		return redact(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode/100 == 2:
		return nil
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode/100 == 5:
		return fmt.Errorf("the webhook answered %s", resp.Status)
	}
	// A redirect is not followed, and a client error comes back the same on a retry.
	return permanent(fmt.Errorf("the webhook answered %s", resp.Status))
}
