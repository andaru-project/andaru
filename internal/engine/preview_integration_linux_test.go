//go:build integration

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andaru-project/andaru/internal/config"
	"github.com/andaru-project/andaru/internal/host"
)

const previewSentinel = "TEST_ONLY_PREVIEW_SECRET"

func ownerTopology(password string) string {
	secret, _ := json.Marshal(password)
	return `{"operations":[
{"op":"create","collection":"zones","localId":"zw","value":{"name":"wan"}},
{"op":"create","collection":"zones","localId":"zl","value":{"name":"lan"}},
{"op":"create","collection":"links","localId":"wan0","value":{"name":"wan0","kind":"ethernet","mtu":1508,"enabled":true,"permanentMac":"52:54:00:aa:bb:01"}},
{"op":"create","collection":"links","localId":"kpn","value":{"name":"kpn","kind":"vlan","mtu":1508,"enabled":true,"parent":"@wan0","vlanId":6}},
{"op":"create","collection":"links","localId":"ppp","value":{"name":"pppoe","kind":"pppoe","mtu":1500,"enabled":true,"parent":"@kpn","username":"kpn@internet","password":{"secret":"WAN_PASSWORD","set":` + string(secret) + `,"expectedVersion":"0"}}},
{"op":"create","collection":"links","localId":"lan0","value":{"name":"lan0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:aa:bb:02"}},
{"op":"create","collection":"segments","localId":"sw","value":{"name":"wan","link":"@ppp","zone":"@zw","addressing":{"ipv4":{"mode":"ppp"},"ipv6":{"mode":"dhcp6","requestPrefixLength":48}}}},
{"op":"create","collection":"segments","localId":"sl","value":{"name":"lan","link":"@lan0","zone":"@zl","addressing":{"ipv4":{"mode":"static","addresses":["192.0.2.1/24"]},"ipv6":{"mode":"delegated","from":"@sw","subnetId":"1","ra":{"enabled":true,"managed":false,"otherInfo":true}}}}}
]}`
}

type previewHost struct {
	linkReads, factReads int
	linkErr, factErr     error
}

func (p *previewHost) links(context.Context) ([]host.LinkStatus, error) {
	p.linkReads++
	return []host.LinkStatus{
		{IfIndex: 2, Name: "ens18", LinkType: "ether", PermanentAddress: "52:54:00:aa:bb:01"},
		{IfIndex: 3, Name: "ens19", LinkType: "ether", PermanentAddress: "52:54:00:aa:bb:02"},
	}, p.linkErr
}
func (p *previewHost) facts() (host.RealizerFacts, error) {
	p.factReads++
	return host.RealizerFacts{ForeignFiles: []string{"/run/systemd/network/10-netplan-eth0.network"}, PPPDaemon: true, PPPoEPlugin: true, PPPDevice: true}, p.factErr
}

func previewFixture(t *testing.T) (http.Handler, *config.Store, *previewHost) {
	t.Helper()
	requireRoot(t)
	s, e := config.Open(context.Background(), filepath.Join(privateTestDir(t), "state"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	h := &previewHost{}
	return newHandler(h.links, newSavedHandler(s, h.links, h.facts, http.NotFoundHandler())), s, h
}

type previewBody struct {
	State        string `json:"state"`
	SavedVersion string `json:"savedVersion"`
	Realizable   *bool  `json:"realizable"`
	Resolved     []any  `json:"resolved"`
	Files        []struct {
		Path, Mode, Content string
		Secret              bool
		Sources             []string
	} `json:"files"`
	Findings []struct{ Severity, Code, Path, Message string } `json:"findings"`
}

func TestPreviewHTTPContract(t *testing.T) {
	h, _, _ := previewFixture(t)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)

	empty := configCall(h, "GET", "/api/v1/preview/network", "", "")
	if empty.Code != 200 || !strings.Contains(empty.Body.String(), `"resolved":[],"files":[]`) || empty.Header().Get("Andaru-Saved-Version") != "0" {
		t.Fatalf("empty store: %d %s", empty.Code, empty.Body)
	}
	if w := configCall(h, "POST", "/api/v1/config/changes", ownerTopology(strings.Repeat("L", 300)+previewSentinel), ""); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	w := configCall(h, "GET", "/api/v1/preview/network", "", "")
	var body previewBody
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	if body.State != "preview" || body.SavedVersion != "1" || w.Header().Get("Andaru-Saved-Version") != "1" || body.Realizable == nil || *body.Realizable || len(body.Resolved) != 2 || len(body.Files) != 7 {
		t.Fatalf("envelope: %+v", body)
	}
	var options, foreign, tooLong bool
	for _, f := range body.Files {
		if strings.HasSuffix(f.Path, ".options") {
			options = f.Secret && f.Mode == "0600" && strings.Contains(f.Content, `password "<redacted>"`) && strings.Contains(f.Content, `user "kpn@internet"`)
		}
	}
	for _, f := range body.Findings {
		foreign = foreign || f.Code == "FOREIGN_CONFIGURATION" && strings.Contains(f.Message, "10-netplan-eth0.network")
		tooLong = tooLong || f.Code == "CREDENTIAL_TOO_LONG" && strings.HasPrefix(f.Path, "/links/") && strings.HasSuffix(f.Path, "/password")
	}
	if !options || !foreign || !tooLong {
		t.Fatalf("options=%v foreign=%v tooLong=%v\n%s", options, foreign, tooLong, w.Body)
	}
	// Publish the documented contract only if the response really has it.
	var openapi struct {
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if e := json.Unmarshal(openAPIDocument, &openapi); e != nil || openapi.Paths["/api/v1/preview/network"] == nil {
		t.Fatalf("preview route is not in the served contract: %v", e)
	}
	var raw map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	assertRequiredSchema(t, openapi.Components.Schemas["PreviewResponse"], raw, []string{"state", "savedVersion", "realizable", "resolved", "files", "findings"})
	assertRequiredSchema(t, openapi.Components.Schemas["RenderedFile"], raw["files"].([]any)[0].(map[string]any), []string{"path", "mode", "secret", "content", "sources"})
	assertRequiredSchema(t, openapi.Components.Schemas["Finding"], raw["findings"].([]any)[0].(map[string]any), []string{"severity", "code", "path", "message"})
	assertRequiredSchema(t, openapi.Components.Schemas["Resolution"], raw["resolved"].([]any)[0].(map[string]any), []string{"link", "interface"})

	head := configCall(h, "HEAD", "/api/v1/preview/network", "", "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Andaru-Saved-Version") != "1" {
		t.Fatalf("HEAD: %d %q", head.Code, head.Body)
	}
	if post := configCall(h, "POST", "/api/v1/preview/network", "{}", ""); post.Code != 405 || post.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST: %d", post.Code)
	}
	for _, query := range []string{"?savedVersion=x", "?savedVersion=1&savedVersion=1", "?other=1", "?savedVersion=01", "?savedVersion=%zz", "?savedVersion=1;x=2", "?savedVersion="} {
		assertAPIError(t, configCall(h, "GET", "/api/v1/preview/network"+query, "", ""), 400, "INVALID_REQUEST")
	}
	assertAPIError(t, configCall(h, "GET", "/api/v1/preview/network?savedVersion=99", "", ""), 404, "NOT_FOUND")
	assertAPIError(t, configCall(h, "GET", "/api/v1/preview/other", "", ""), 404, "NOT_FOUND")
	if all := w.Body.String() + head.Body.String() + logs.String(); strings.Contains(all, previewSentinel) {
		t.Fatal("secret reached a preview response or the log")
	}
}

func TestPreviewUsesTheRequestedVersionsOwnSecret(t *testing.T) {
	h, s, _ := previewFixture(t)
	ctx := context.Background()
	if w := configCall(h, "POST", "/api/v1/config/changes", ownerTopology(strings.Repeat("L", 300)), ""); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	list, e := s.List(ctx, "links")
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range list.Items {
		var value map[string]any
		_ = json.Unmarshal(item.Value, &value)
		if value["kind"] != "pppoe" {
			continue
		}
		value["password"] = map[string]any{"secret": "WAN_PASSWORD", "set": "kpn", "expectedVersion": "1"}
		raw, _ := json.Marshal(value)
		change, e := config.ObjectChange("replace", "links", value["id"].(string), &item.Version, raw)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Save(ctx, "unix:uid:0", change); e != nil {
			t.Fatal(e)
		}
	}
	old := configCall(h, "GET", "/api/v1/preview/network?savedVersion=1", "", "")
	now := configCall(h, "GET", "/api/v1/preview/network", "", "")
	if old.Code != 200 || !strings.Contains(old.Body.String(), "CREDENTIAL_TOO_LONG") || old.Header().Get("Andaru-Saved-Version") != "1" {
		t.Fatalf("version 1 lost its own password: %d %s", old.Code, old.Body)
	}
	if now.Code != 200 || strings.Contains(now.Body.String(), "CREDENTIAL_TOO_LONG") || now.Header().Get("Andaru-Saved-Version") != "2" {
		t.Fatalf("head: %d %s", now.Code, now.Body)
	}
}

func TestPreviewFailuresAndAuthorization(t *testing.T) {
	h, _, observed := previewFixture(t)
	observed.linkErr = errors.New("private tool stderr")
	w := configCall(h, "GET", "/api/v1/preview/network", "", "")
	assertAPIError(t, w, 503, "INVENTORY_UNAVAILABLE")
	if strings.Contains(w.Body.String(), "private tool stderr") || strings.Contains(w.Body.String(), `"files"`) {
		t.Fatalf("failure leaked detail or a partial preview: %s", w.Body)
	}
	observed.linkErr, observed.factErr = nil, errors.New("private directory error")
	assertAPIError(t, configCall(h, "GET", "/api/v1/preview/network", "", ""), 503, "INVENTORY_UNAVAILABLE")
	observed.linkErr = errors.New("private tool stderr")
	// The saved version is checked before the host is read.
	assertAPIError(t, configCall(h, "GET", "/api/v1/preview/network?savedVersion=99", "", ""), 404, "NOT_FOUND")
	before := *observed
	r := rootRequest("GET", "/api/v1/preview/network")
	r = r.WithContext(context.WithValue(context.Background(), peerKey{}, peerIdentity{uid: 65534, verified: true}))
	denied := httptest.NewRecorder()
	h.ServeHTTP(denied, r)
	assertAPIError(t, denied, 403, "FORBIDDEN")
	if observed.linkReads != before.linkReads || observed.factReads != before.factReads {
		t.Fatal("a non-root peer caused a host read")
	}
}
