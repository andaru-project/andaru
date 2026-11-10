package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/andaru-project/andaru/internal/config"
)

type ctlRequest struct {
	method, path, kind, tag string
	body                    bool
	status                  int
}

var objectIDPattern = regexp.MustCompile(`^(lnk|seg|zon)_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var errUsage = errors.New("usage: andaru ctl [--socket PATH] links|openapi|config <show|list|get|create|replace|delete|changes>|versions <list|get>|preview network [--saved-version N]")

func collectionName(s string) bool { return s == "links" || s == "segments" || s == "zones" }
func ctlOperation(args []string, stderr io.Writer) (ctlRequest, error) {
	req := ctlRequest{method: "GET", status: 200}
	if len(args) == 1 {
		switch args[0] {
		case "links":
			req.path = "/api/v1/status/links"
			req.kind = "links"
			return req, nil
		case "openapi":
			req.path = "/api/v1/openapi.json"
			req.kind = "openapi"
			return req, nil
		}
	}
	if len(args) < 2 {
		return req, errUsage
	}
	if args[0] == "config" {
		verb := args[1]
		leaf := args[2:]
		version := ""
		if verb == "replace" || verb == "delete" {
			fs := flag.NewFlagSet(verb, flag.ContinueOnError)
			fs.SetOutput(stderr)
			fs.StringVar(&version, "version", "", "explicit version returned by the object read")
			if e := fs.Parse(leaf); e != nil {
				return req, e
			}
			leaf = fs.Args()
			if _, e := config.ParseVersion(version); e != nil {
				return req, errUsage
			}
		}
		if verb == "show" && len(leaf) == 0 {
			req.path = "/api/v1/config"
			req.kind = "snapshot"
			return req, nil
		}
		if verb == "changes" && len(leaf) == 0 {
			req.method = "POST"
			req.path = "/api/v1/config/changes"
			req.kind = "save"
			req.body = true
			return req, nil
		}
		if len(leaf) < 1 || !collectionName(leaf[0]) {
			return req, errUsage
		}
		col := leaf[0]
		base := "/api/v1/config/" + col
		switch verb {
		case "list":
			if len(leaf) != 1 {
				return req, errUsage
			}
			req.path = base
			req.kind = "collection"
		case "create":
			if len(leaf) != 1 {
				return req, errUsage
			}
			req.method = "POST"
			req.path = base
			req.body = true
			req.status = 201
			req.kind = "object"
		case "get", "replace", "delete":
			if len(leaf) != 2 || !objectIDPattern.MatchString(leaf[1]) {
				return req, errUsage
			}
			req.path = base + "/" + url.PathEscape(leaf[1])
			req.kind = "object"
			if verb != "get" {
				req.tag = `"` + leaf[1] + "." + version + `"`
				if verb == "replace" {
					req.method = "PUT"
					req.body = true
				} else {
					req.method = "DELETE"
					req.kind = "ack"
				}
			}
		default:
			return req, errUsage
		}
		return req, nil
	}
	if args[0] == "preview" {
		if args[1] != "network" {
			return req, errUsage
		}
		fs := flag.NewFlagSet("preview network", flag.ContinueOnError)
		fs.SetOutput(stderr)
		saved := fs.String("saved-version", "", "saved version to render (default: the saved head)")
		if e := fs.Parse(args[2:]); e != nil {
			return req, e
		}
		if fs.NArg() != 0 {
			return req, errUsage
		}
		req.path = "/api/v1/preview/network"
		req.kind = "preview"
		if *saved != "" {
			v, e := config.ParseVersion(*saved)
			if e != nil {
				return req, errUsage
			}
			req.path += "?" + url.Values{"savedVersion": {string(v)}}.Encode()
		}
		return req, nil
	}
	if args[0] == "versions" {
		if args[1] == "get" && len(args) == 3 {
			v, e := config.ParseVersion(args[2])
			if e != nil {
				return req, errUsage
			}
			req.path = "/api/v1/versions/" + url.PathEscape(string(v))
			req.kind = "version"
			return req, nil
		}
		if args[1] != "list" {
			return req, errUsage
		}
		fs := flag.NewFlagSet("versions list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		limit := fs.Int("limit", 50, "maximum saved versions (1-100)")
		before := fs.String("before", "", "exclusive saved version cursor")
		if e := fs.Parse(args[2:]); e != nil {
			return req, e
		}
		if fs.NArg() != 0 || *limit < 1 || *limit > 100 {
			return req, errUsage
		}
		q := url.Values{"limit": {strconv.Itoa(*limit)}}
		if *before != "" {
			v, e := config.ParseVersion(*before)
			if e != nil {
				return req, errUsage
			}
			q.Set("before", string(v))
		}
		req.path = "/api/v1/versions?" + q.Encode()
		req.kind = "history"
		return req, nil
	}
	return req, errUsage
}

// readInput bounds stdin to the request limit and abandons the read when ctx ends.
// shortcut: a blocked read keeps its goroutine until the reader unblocks; the CLI exits right after, so upgrade only if runCtl becomes long-lived.
func readInput(ctx context.Context, r io.Reader) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
		done <- result{data, err}
	}()
	select {
	case res := <-done:
		return res.data, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func runCtl(ctx context.Context, socket string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	spec, e := ctlOperation(args, stderr)
	if e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, errUsage)
		return 2
	}
	var payload []byte
	if spec.body {
		payload, e = readInput(ctx, stdin)
		if e != nil || len(payload) > 1<<20 {
			fmt.Fprintln(stderr, "request input could not be read within its size limit")
			return 1
		}
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	timeout := 5 * time.Second
	if spec.method != "GET" {
		timeout = 10 * time.Second
	}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, e := http.NewRequestWithContext(ctx, spec.method, "http://unix"+spec.path, bytes.NewReader(payload))
	if e != nil {
		fmt.Fprintln(stderr, "could not construct local engine request")
		return 1
	}
	if spec.body {
		request.Header.Set("Content-Type", "application/json")
	}
	if spec.tag != "" {
		request.Header.Set("If-Match", spec.tag)
	}
	response, e := client.Do(request)
	if e != nil {
		if spec.method == "GET" {
			fmt.Fprintln(stderr, "could not reach the local engine")
		} else {
			fmt.Fprintln(stderr, "save response unavailable; outcome may be unknown; read current state before retrying")
		}
		return 1
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if e != nil || len(body) > responseLimit {
		fmt.Fprintln(stderr, "engine response exceeded its read/size budget")
		return 1
	}
	if response.StatusCode != spec.status {
		if jsontext.Value(body).IsValid() {
			fmt.Fprintln(stderr, string(body))
		} else {
			fmt.Fprintf(stderr, "engine returned HTTP %d\n", response.StatusCode)
		}
		return 1
	}
	if !validEnvelope(spec.kind, body) {
		fmt.Fprintln(stderr, "engine returned an invalid "+spec.kind+" response")
		return 1
	}
	if n, e := stdout.Write(body); e != nil || n != len(body) {
		fmt.Fprintln(stderr, "could not write engine response")
		return 1
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		if _, e := io.WriteString(stdout, "\n"); e != nil {
			fmt.Fprintln(stderr, "could not finish engine response")
			return 1
		}
	}
	return 0
}
func validVersion(v config.Version) bool { _, e := config.ParseVersion(string(v)); return e == nil }
func validObject(v config.ObjectView) bool {
	if !validVersion(v.Version) || v.Version == "0" {
		return false
	}
	var value struct{ ID, Name string }
	if e := json.Unmarshal(v.Value, &value); e != nil {
		return false
	}
	return objectIDPattern.MatchString(value.ID) && value.Name != ""
}
func validDeclarationView(raw json.RawMessage) bool {
	var d struct {
		SchemaVersion          int
		Links, Segments, Zones []json.RawMessage
	}
	return json.Unmarshal(raw, &d) == nil && d.SchemaVersion == 1 && d.Links != nil && d.Segments != nil && d.Zones != nil
}
func validEnvelope(kind string, body []byte) bool {
	if !jsontext.Value(body).IsValid() {
		return false
	}
	var document map[string]json.RawMessage
	if e := json.Unmarshal(body, &document); e != nil || document == nil {
		return false
	}
	switch kind {
	case "links":
		var items []json.RawMessage
		return json.Unmarshal(document["items"], &items) == nil && items != nil
	case "openapi":
		var version string
		var paths map[string]json.RawMessage
		return json.Unmarshal(document["openapi"], &version) == nil && version != "" && json.Unmarshal(document["paths"], &paths) == nil && paths != nil
	case "object":
		var v config.ObjectView
		return json.Unmarshal(body, &v) == nil && validObject(v)
	case "snapshot":
		var v config.SnapshotView
		return json.Unmarshal(body, &v) == nil && v.State == "saved" && validVersion(v.SavedVersion) && v.UnusedSecrets != nil && validDeclarationView(v.Value)
	case "collection":
		var v config.CollectionView
		if json.Unmarshal(body, &v) != nil || v.State != "saved" || !validVersion(v.SavedVersion) || v.Items == nil {
			return false
		}
		for _, item := range v.Items {
			if !validObject(item) {
				return false
			}
		}
		return true
	case "save":
		var v config.SaveResult
		if json.Unmarshal(body, &v) != nil || v.State != "saved" || !validVersion(v.SavedVersion) || v.Items == nil || v.Created == nil {
			return false
		}
		for _, item := range v.Items {
			if !objectIDPattern.MatchString(item.ID) || !collectionName(item.Collection) {
				return false
			}
			if !item.Deleted && !validObject(config.ObjectView{Value: item.Value, Version: item.Version}) {
				return false
			}
		}
		return true
	case "ack":
		var v struct {
			State        string
			SavedVersion config.Version
		}
		return json.Unmarshal(body, &v) == nil && v.State == "saved" && validVersion(v.SavedVersion)
	case "history":
		var v config.HistoryView
		if json.Unmarshal(body, &v) != nil || v.State != "saved" || v.Items == nil {
			return false
		}
		for _, item := range v.Items {
			if !validVersion(item.ID) || item.ChangedObjectCount < 0 {
				return false
			}
		}
		return v.NextBefore == nil || validVersion(*v.NextBefore)
	case "preview":
		var v struct {
			State                     string
			SavedVersion              config.Version
			Realizable                *bool
			Resolved, Files, Findings []json.RawMessage
		}
		return json.Unmarshal(body, &v) == nil && v.State == "preview" && validVersion(v.SavedVersion) && v.Realizable != nil && v.Resolved != nil && v.Files != nil && v.Findings != nil
	case "version":
		var v config.VersionView
		return json.Unmarshal(body, &v) == nil && v.State == "saved" && validVersion(v.ID) && v.Changes != nil && validDeclarationView(v.Value)
	}
	return false
}
