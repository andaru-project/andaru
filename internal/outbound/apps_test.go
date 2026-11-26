package outbound

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/delivery"
	"github.com/andaru-project/andaru/internal/notify"
)

func TestPriorities(t *testing.T) {
	for _, row := range []struct {
		severity       string
		ntfy, pushover int
	}{{notify.Info, 3, 0}, {notify.Warning, 4, 0}, {notify.Critical, 5, 1}, {"", 3, 0}} {
		if n, p := ntfyPriority(row.severity), pushoverPriority(row.severity); n != row.ntfy || p != row.pushover {
			t.Errorf("%q: ntfy %d, Pushover %d", row.severity, n, p)
		}
	}
}

func TestMessageText(t *testing.T) {
	for _, row := range []struct {
		m          delivery.Message
		link, want string
	}{
		{delivery.Message{Subject: "Disk <full> & hot"}, "", "<b>Disk &lt;full&gt; &amp; hot</b>"},
		{delivery.Message{Subject: "s", Detail: "a\n\"b\""}, "/system?x=1&y=2", "<b>s</b>\n\na\n&#34;b&#34;\n\n/system?x=1&amp;y=2"},
		{delivery.Message{Subject: "s", Detail: strings.Repeat("é", 2000)}, "", "<b>s</b>\n\n" + strings.Repeat("é", 1024)},
	} {
		if got := telegramText(row.m, row.link); got != row.want {
			t.Errorf("telegramText %+v = %q", row.m, got)
		}
	}
	if text(delivery.Message{Subject: "s"}) != "s" || text(delivery.Message{Subject: "s", Detail: "d"}) != "d" {
		t.Error("text")
	}
	m := delivery.Message{Link: "/system/notifications"}
	if link(delivery.Job{Message: m}) != "" || link(delivery.Job{Origin: "https://fw.example/", Message: m}) != "https://fw.example/system/notifications" ||
		link(delivery.Job{Origin: "https://fw.example"}) != "" {
		t.Error("link")
	}
	if cut("aé", 2) != "a" || cut("aé", 3) != "aé" {
		t.Error("cut")
	}
}

type request struct {
	method, path string
	header       http.Header
	body         string
}

// service answers each request with the next status and body, and records it.
func service(t *testing.T, answers ...string) (string, chan request) {
	t.Helper()
	got := make(chan request, len(answers))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- request{r.Method, r.URL.EscapedPath(), r.Header, string(body)}
		status, answer, _ := strings.Cut(answers[0], " ")
		answers = answers[1:]
		code := map[string]int{"200": 200, "302": 302, "400": 400, "401": 401, "403": 403, "404": 404, "429": 429, "500": 500}[status]
		if code == 302 {
			w.Header().Set("Location", "https://elsewhere.example/")
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, got
}

func appJob(kind string) delivery.Job {
	return delivery.Job{ID: "j", Channel: "ntf_x", Kind: kind, Origin: "https://fw.example",
		Message: delivery.Message{Source: "apply", Key: "k", Severity: notify.Critical, Subject: "Disk full", Detail: "<b>99%</b>", Link: "/system", Raised: time.Unix(1, 0)}}
}

// Each app is sent its message in its own form, its answers are judged as accepted, retried or permanent, and no
// error shows a token or a URL.
func TestAppSenders(t *testing.T) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer func(p, tg string) { pushoverAPI, telegramAPI = p, tg }(pushoverAPI, telegramAPI)
	const secret = "SECRET_TOKEN"
	answers := []string{"429 ", "500 ", "400 ", "401 ", "403 ", "404 ", "302 "}
	verdicts := []string{"", "retry", "retry", "permanent", "permanent", "permanent", "permanent", "permanent"}
	for _, row := range []struct {
		kind, accept, refuse string
		target               func(base string, job *delivery.Job)
		check                func(r request) bool
	}{
		{delivery.Ntfy, "{}", "", func(base string, j *delivery.Job) {
			j.Ntfy = &delivery.NtfyTarget{Server: base + "/sub/", Topic: "alerts", Token: secret}
		}, func(r request) bool {
			var m map[string]any
			return r.path == "/sub/" && r.header.Get("Authorization") == "Bearer "+secret && r.header.Get("Content-Type") == "application/json" &&
				json.Unmarshal([]byte(r.body), &m) == nil && m["topic"] == "alerts" && m["title"] == "Disk full" && m["message"] == "<b>99%</b>" &&
				m["priority"] == 5.0 && m["click"] == "https://fw.example/system" && len(m["tags"].([]any)) == 1
		}},
		{delivery.Pushover, `{"status":1,"request":"r"}`, `{"status":0}`, func(base string, j *delivery.Job) {
			pushoverAPI = base + "/1/messages.json"
			j.Pushover = &delivery.PushoverTarget{Token: secret, User: "USER_KEY"}
		}, func(r request) bool {
			f, err := url.ParseQuery(r.body)
			return err == nil && r.path == "/1/messages.json" && r.header.Get("Content-Type") == "application/x-www-form-urlencoded" &&
				f.Get("token") == secret && f.Get("user") == "USER_KEY" && f.Get("title") == "Disk full" && f.Get("message") == "<b>99%</b>" &&
				f.Get("priority") == "1" && f.Get("url") == "https://fw.example/system" && len(f) == 6
		}},
		{delivery.Telegram, `{"ok":true,"result":{}}`, `{"ok":false}`, func(base string, j *delivery.Job) {
			telegramAPI = base
			j.Telegram = &delivery.TelegramTarget{Token: "1:" + secret, ChatID: "-100"}
		}, func(r request) bool {
			var m map[string]any
			return r.path == "/bot1:"+secret+"/sendMessage" && r.header.Get("Content-Type") == "application/json" &&
				json.Unmarshal([]byte(r.body), &m) == nil && m["chat_id"] == "-100" && m["parse_mode"] == "HTML" && m["disable_web_page_preview"] == true &&
				m["text"] == "<b>Disk full</b>\n\n&lt;b&gt;99%&lt;/b&gt;\n\nhttps://fw.example/system"
		}},
	} {
		t.Run(row.kind, func(t *testing.T) {
			list, want := append([]string{"200 " + row.accept}, answers...), verdicts
			if row.refuse != "" { // a 2xx that does not say the message was taken
				list, want = append(list, "200 "+row.refuse), append(slices.Clip(want), "permanent")
			}
			base, got := service(t, list...)
			for i := range list {
				job := appJob(row.kind)
				row.target(base, &job)
				err := (&Service{HTTP: client}).send(context.Background(), job)
				if r := <-got; r.method != http.MethodPost || !row.check(r) {
					t.Fatalf("request %+v", r)
				}
				verdict := ""
				if err != nil {
					verdict = map[bool]string{true: "permanent", false: "retry"}[isPermanent(err)]
					if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "127.0.0.1") {
						t.Errorf("error shows the target: %v", err)
					}
				}
				if verdict != want[i] {
					t.Errorf("answer %q: %v, want %s", list[i], err, want[i])
				}
			}
			// A failed connection shows the host at most: never the token, nor the URL.
			job := appJob(row.kind)
			row.target("http://127.0.0.1:1", &job)
			if err := (&Service{HTTP: client}).send(context.Background(), job); err == nil || isPermanent(err) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "http://") {
				t.Errorf("failed connection: %v", err)
			}
			if err := (&Service{HTTP: client}).send(context.Background(), delivery.Job{Kind: row.kind}); err == nil || !isPermanent(err) {
				t.Errorf("without a target: %v", err)
			}
		})
	}
}

// A credential with a control character, as a pasted token's trailing newline, is given up at once, not retried for
// an hour: every attempt would fail alike.
func TestControlCharacterInCredentialIsPermanent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	defer func(p, tg string) { pushoverAPI, telegramAPI = p, tg }(pushoverAPI, telegramAPI)
	pushoverAPI, telegramAPI = srv.URL, srv.URL
	m := delivery.Message{Subject: "s"}
	for _, send := range []func() error{
		func() error {
			return sendNtfy(context.Background(), srv.Client(), delivery.Job{Message: m, Ntfy: &delivery.NtfyTarget{Server: srv.URL, Topic: "t", Token: "tk_abc\n"}})
		},
		func() error {
			return sendPushover(context.Background(), srv.Client(), delivery.Job{Message: m, Pushover: &delivery.PushoverTarget{Token: "tk_abc\n", User: "u"}})
		},
		func() error {
			return sendTelegram(context.Background(), srv.Client(), delivery.Job{Message: m, Telegram: &delivery.TelegramTarget{Token: "1:tk_abc\n", ChatID: "-1"}})
		},
	} {
		if err := send(); !isPermanent(err) || strings.Contains(err.Error(), "tk_abc") {
			t.Fatalf("%v", err)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d calls", n)
	}
	// A tab is a valid header character.
	if credential("x", "Bearer\tabc") != nil {
		t.Fatal("a tab was refused")
	}
}

// A 2xx whose body is cut off has not said whether the message was taken: it is retried, not given up.
func TestCutOffAnswerIsRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":`))
		hj, _ := w.(http.Hijacker)
		if c, _, err := hj.Hijack(); err == nil {
			c.Close()
		}
	}))
	defer srv.Close()
	defer func(tg string) { telegramAPI = tg }(telegramAPI)
	telegramAPI = srv.URL
	err := sendTelegram(context.Background(), srv.Client(), delivery.Job{Message: delivery.Message{Subject: "s"}, Telegram: &delivery.TelegramTarget{Token: "1:x", ChatID: "-1"}})
	if err == nil || isPermanent(err) {
		t.Fatalf("%v", err)
	}
}
