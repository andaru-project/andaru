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

// sendWebhook posts the message's JSON. Any 2xx answer is acceptance; the body is not read beyond a little, since it
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
	body, err := json.Marshal(job.Message)
	if err != nil {
		return permanent(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.URL, bytes.NewReader(body))
	if err != nil {
		return permanent(err)
	}
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the webhook answered %s", resp.Status)
	}
	return nil
}
