// SPDX-License-Identifier: AGPL-3.0-or-later
package driver

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// NewHTTPClient returns a per-poll HTTP client honouring the insecure_skip_verify setting.
// It refuses redirects to another host or from https to http so API key headers and login bodies stay on the target.
// Callers must call CloseIdleConnections when done.
func NewHTTPClient(cfg TargetConfig, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.BoolSetting("insecure_skip_verify", false)}, //nolint:gosec // user-configurable for local self-signed devices
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if !sameHost(req.URL, via[0].URL) {
				return fmt.Errorf("refusing redirect from %s to different host %s", via[0].URL.Host, req.URL.Host)
			}
			if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect from https to %s", req.URL.Scheme)
			}
			return nil
		},
	}
}

// CheckCredentialTransport refuses to send credentials to a plain http:// endpoint unless the
// target opts in with allow_insecure_http: true. https:// and unix:// endpoints pass.
func CheckCredentialTransport(cfg TargetConfig) error {
	u, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil {
		return fmt.Errorf("target %q: invalid endpoint: %w", cfg.ID, err)
	}
	if strings.EqualFold(u.Scheme, "http") && !cfg.BoolSetting("allow_insecure_http", false) {
		return fmt.Errorf("target %q: refusing to send credentials over plain http; use https or set allow_insecure_http: true", cfg.ID)
	}
	return nil
}

// sameHost reports whether next stays on first's host: hostnames compare case-insensitively
// and an omitted port means the scheme default.
func sameHost(next, first *url.URL) bool {
	if !strings.EqualFold(next.Hostname(), first.Hostname()) {
		return false
	}
	np, fp := effectivePort(next), effectivePort(first)
	// http://h -> https://h moves from :80 to :443 without changing host.
	return np == fp || (first.Scheme == "http" && fp == "80" && next.Scheme == "https" && np == "443")
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// DoJSON sends body (when not nil) as JSON to url, with auth (when not nil) adding the
// credentials, and decodes the response into out. A non-2xx answer is a *StatusError.
func DoJSON(ctx context.Context, client *http.Client, method, url string, auth func(*http.Request), body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return err
	}
	// OPNsense rejects an empty body sent as JSON, so only a real body gets the header.
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != nil {
		auth(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// REST APIs explain a refusal in "message" (pfSense, OPNsense auth failures) or
		// "errorMessage" (OPNsense application errors).
		var e struct {
			Message      string `json:"message"`
			ErrorMessage string `json:"errorMessage"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		return &StatusError{Code: resp.StatusCode, Message: cleanMessage(cmp.Or(e.Message, e.ErrorMessage))}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

// cleanMessage makes a device's error text safe for logs and the UI: control and line or
// paragraph separator characters become spaces, format characters (bidi, zero-width) go, and
// the text is cut to 300 characters.
func cleanMessage(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp):
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return strings.TrimSpace(s)
}

// StatusError is a non-2xx HTTP answer.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.Code)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Code, e.Message)
}

// HTTPStatus returns the status code of a *StatusError in err's chain, or 0.
func HTTPStatus(err error) int {
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.Code
	}
	return 0
}

// Sleep waits for d or until ctx is done, returning ctx's error then.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
