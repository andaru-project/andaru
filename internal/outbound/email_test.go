package outbound

import (
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andaru-project/andaru/internal/delivery"
)

// fakeSMTP is an SMTP server for mail.test, enough of one for net/smtp: EHLO, STARTTLS, AUTH PLAIN, MAIL, RCPT, DATA.
type fakeSMTP struct {
	implicit, starttls bool
	reply              map[string]string // a command's reply in place of its own, by verb
	auth               string            // the AUTH mechanisms offered; PLAIN when empty
	rcpt               map[string]string // a RCPT's reply in place of 250, by the address it names
	dataEnd            string            // the reply to the end of DATA in place of 250
	mu                 sync.Mutex
	commands           []string
	data               string
}

// selfSigned is a certificate for mail.test and a pool that trusts it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"mail.test"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// start serves f and points the sender at it, trusting its certificate; it returns the address the sender dialled.
func (f *fakeSMTP) start(t *testing.T) *string {
	cert, pool := selfSigned(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dialled := new(string)
	oldDial, oldRoots := smtpDial, smtpRoots
	smtpDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		*dialled = address
		return (&net.Dialer{}).DialContext(ctx, network, l.Addr().String())
	}
	smtpRoots = pool
	t.Cleanup(func() { smtpDial, smtpRoots = oldDial, oldRoots; l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go f.serve(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
		}
	}()
	return dialled
}

func (f *fakeSMTP) serve(conn net.Conn, config *tls.Config) {
	defer conn.Close()
	secure := f.implicit
	if secure {
		conn = tls.Server(conn, config)
	}
	tp := textproto.NewConn(conn)
	_ = tp.PrintfLine("220 mail.test ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		f.mu.Lock()
		f.commands = append(f.commands, line)
		f.mu.Unlock()
		if r, ok := f.reply[verb]; ok {
			_ = tp.PrintfLine("%s", r)
			continue
		}
		switch verb {
		case "EHLO":
			if f.starttls && !secure {
				_ = tp.PrintfLine("250-mail.test")
				_ = tp.PrintfLine("250-STARTTLS")
			} else {
				_ = tp.PrintfLine("250-mail.test")
			}
			_ = tp.PrintfLine("250 AUTH %s", cmp.Or(f.auth, "PLAIN"))
		case "STARTTLS":
			_ = tp.PrintfLine("220 go ahead")
			conn = tls.Server(conn, config)
			tp, secure = textproto.NewConn(conn), true
		case "AUTH":
			if strings.Contains(strings.ToUpper(line), "LOGIN") {
				_ = tp.PrintfLine("334 VXNlcm5hbWU6")
				user, _ := tp.ReadLine()
				_ = tp.PrintfLine("334 UGFzc3dvcmQ6")
				password, _ := tp.ReadLine()
				f.mu.Lock()
				f.commands = append(f.commands, "LOGIN "+user+" "+password)
				f.mu.Unlock()
			}
			_ = tp.PrintfLine("235 2.7.0 accepted")
		case "MAIL":
			_ = tp.PrintfLine("250 ok")
		case "RCPT":
			r := "250 ok"
			for address, refusal := range f.rcpt {
				if strings.Contains(line, address) {
					r = refusal
				}
			}
			_ = tp.PrintfLine("%s", r)
		case "DATA":
			_ = tp.PrintfLine("354 go ahead")
			lines, _ := tp.ReadDotLines()
			f.mu.Lock()
			f.data = strings.Join(lines, "\r\n")
			f.mu.Unlock()
			_ = tp.PrintfLine("%s", cmp.Or(f.dataEnd, "250 queued"))
		case "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("502 unknown")
		}
	}
}

func emailJob(security string) delivery.Job {
	return delivery.Job{ID: "evt_1:ntf_2", Kind: delivery.Email, Origin: "https://fw.example",
		Message: delivery.Message{Subject: "Disk é full\r\nBcc: evil@example.com", Detail: "Only 1% left.", Link: "/system"},
		Email: &delivery.EmailTarget{Host: "mail.test", Port: 465, Security: security, Username: "alerts@example.com",
			Password: "hunter2-SECRET", From: "Andaru <alerts@example.com>", To: []string{"ops@example.com", "Two <two@example.com>"}}}
}

func TestEmailSends(t *testing.T) {
	for _, f := range []*fakeSMTP{{implicit: true}, {starttls: true}} {
		security := map[bool]string{true: "tls", false: "starttls"}[f.implicit]
		dialled := f.start(t)
		if err := sendEmail(context.Background(), emailJob(security)); err != nil {
			t.Fatalf("%s: %v", security, err)
		}
		if *dialled != "mail.test:465" {
			t.Errorf("%s: dialled %s", security, *dialled)
		}
		f.mu.Lock()
		commands := strings.Join(f.commands, "\n")
		data := f.data
		f.mu.Unlock()
		plain := base64.StdEncoding.EncodeToString([]byte("\x00alerts@example.com\x00hunter2-SECRET"))
		for _, want := range []string{"AUTH PLAIN " + plain, "MAIL FROM:<alerts@example.com>", "RCPT TO:<ops@example.com>", "RCPT TO:<two@example.com>"} {
			if !strings.Contains(commands, want) {
				t.Errorf("%s: no %q in\n%s", security, want, commands)
			}
		}
		if security == "starttls" && !strings.Contains(commands, "STARTTLS") {
			t.Errorf("no STARTTLS in\n%s", commands)
		}
		msg, err := mail.ReadMessage(strings.NewReader(data + "\r\n"))
		if err != nil {
			t.Fatalf("%s: %v\n%s", security, err, data)
		}
		subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
		if err != nil || subject != "Disk é full\r\nBcc: evil@example.com" || msg.Header.Get("Bcc") != "" {
			t.Errorf("%s: subject %q %v, header %v", security, subject, err, msg.Header)
		}
		if msg.Header.Get("From") != `"Andaru" <alerts@example.com>` || msg.Header.Get("To") != `<ops@example.com>, "Two" <two@example.com>` ||
			msg.Header.Get("Message-ID") != "<evt_1-ntf_2@example.com>" || msg.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("%s: header %v", security, msg.Header)
		}
		if _, err := mail.ParseDate(msg.Header.Get("Date")); err != nil {
			t.Errorf("%s: date %v", security, err)
		}
		body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
		if want := "Only 1% left.\r\n\r\nhttps://fw.example/system\r\n\r\n-- \r\nSent by Andaru\r\n"; string(body) != want {
			t.Errorf("%s: body %q", security, body)
		}
	}
}

// Without an origin the link is sent as the web UI's path; without a user name the server is sent no credentials.
func TestEmailWithoutOriginOrCredentials(t *testing.T) {
	f := &fakeSMTP{implicit: true}
	f.start(t)
	job := emailJob("tls")
	job.Origin, job.Email.Username, job.Email.Password = "", "", ""
	if err := sendEmail(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(strings.Join(f.commands, "\n"), "AUTH") || !strings.Contains(f.data, "\r\n/system\r\n") {
		t.Fatalf("%q\n%s", f.commands, f.data)
	}
}

func TestEmailFailures(t *testing.T) {
	for name, row := range map[string]struct {
		f         *fakeSMTP
		security  string
		permanent bool
		trustNone bool
	}{
		"no STARTTLS":       {&fakeSMTP{}, "starttls", true, false},
		"4xx":               {&fakeSMTP{implicit: true, reply: map[string]string{"MAIL": "451 4.3.0 try later"}}, "tls", false, false},
		"4xx on RCPT":       {&fakeSMTP{starttls: true, reply: map[string]string{"RCPT": "452 4.2.2 mailbox full"}}, "starttls", false, false},
		"5xx":               {&fakeSMTP{implicit: true, reply: map[string]string{"RCPT": "550 5.1.1 no such user"}}, "tls", true, false},
		"421 greeting":      {&fakeSMTP{implicit: true, reply: map[string]string{"EHLO": "421 4.3.2 busy", "HELO": "421 4.3.2 busy"}}, "tls", false, false},
		"refused at end":    {&fakeSMTP{implicit: true, dataEnd: "554 5.7.1 spam"}, "tls", true, false},
		"no PLAIN or LOGIN": {&fakeSMTP{implicit: true, auth: "CRAM-MD5"}, "tls", true, false},
		"auth failure":      {&fakeSMTP{implicit: true, reply: map[string]string{"AUTH": "535 5.7.8 Authentication failed"}}, "tls", true, false},
		"untrusted":         {&fakeSMTP{implicit: true}, "tls", true, true},
		"untrusted TLS":     {&fakeSMTP{starttls: true}, "starttls", true, true},
	} {
		t.Run(name, func(t *testing.T) {
			row.f.start(t)
			if row.trustNone {
				smtpRoots = x509.NewCertPool()
			}
			err := sendEmail(context.Background(), emailJob(row.security))
			if err == nil || isPermanent(err) != row.permanent {
				t.Fatalf("%v, permanent %v", err, isPermanent(err))
			}
			if plain := base64.StdEncoding.EncodeToString([]byte("\x00alerts@example.com\x00hunter2-SECRET")); strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), plain) {
				t.Fatalf("the password in %v", err)
			}
			row.f.mu.Lock()
			defer row.f.mu.Unlock()
			if row.f.data != "" && row.f.dataEnd == "" {
				t.Fatal("a message was sent")
			}
		})
	}
}

// A server that cannot be reached is retried.
func TestEmailUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	old := smtpDial
	smtpDial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	t.Cleanup(func() { smtpDial = old })
	if err := sendEmail(context.Background(), emailJob("tls")); err == nil || isPermanent(err) {
		t.Fatalf("%v", err)
	}
	job := emailJob("none")
	if err := sendEmail(context.Background(), job); !isPermanent(err) {
		t.Fatalf("security none: %v", err)
	}
}

// A server that offers only AUTH LOGIN is logged in to with it, over TLS.
func TestEmailAuthLogin(t *testing.T) {
	f := &fakeSMTP{implicit: true, auth: "LOGIN"}
	f.start(t)
	if err := sendEmail(context.Background(), emailJob("tls")); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	user, password := base64.StdEncoding.EncodeToString([]byte("alerts@example.com")), base64.StdEncoding.EncodeToString([]byte("hunter2-SECRET"))
	if !slices.Contains(f.commands, "LOGIN "+user+" "+password) || f.data == "" {
		t.Fatalf("commands %q", f.commands)
	}
}

// A recipient refused for good keeps no other from the message.
func TestEmailRefusedRecipientKeepsTheOthers(t *testing.T) {
	f := &fakeSMTP{implicit: true, rcpt: map[string]string{"two@example.com": "550 5.1.1 no such user"}}
	f.start(t)
	if err := sendEmail(context.Background(), emailJob("tls")); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data == "" {
		t.Fatal("nothing was sent")
	}
}
