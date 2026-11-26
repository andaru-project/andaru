package outbound

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/andaru-project/andaru/internal/delivery"
)

// Tests reach their own SMTP server through these: smtpDial connects, and smtpRoots, nil for the system's, verifies
// its certificate.
var (
	smtpDial  = (&net.Dialer{}).DialContext
	smtpRoots *x509.CertPool
)

// sendEmail sends the message to the SMTP server over implicit TLS or STARTTLS, never in the clear, with the server's
// certificate verified against its host name, logging in with AUTH PLAIN or, failing that, AUTH LOGIN. A 4xx answer and
// a network failure are retried; a 5xx answer, a failed verification and a server without STARTTLS are not. A
// recipient refused for good keeps no other from the message: then the message is sent, and a partialError says who
// was refused.
func sendEmail(ctx context.Context, job delivery.Job) (result error) {
	t := job.Email
	if t == nil || t.Host == "" || len(t.To) == 0 || (t.Security != "tls" && t.Security != "starttls") {
		return permanent(errors.New("an e-mail job without its server, security or recipients"))
	}
	from, err := mail.ParseAddress(t.From)
	if err != nil {
		return permanent(errors.New("the e-mail sender is not an address"))
	}
	to := make([]*mail.Address, len(t.To))
	for i, s := range t.To {
		if to[i], err = mail.ParseAddress(s); err != nil {
			return permanent(errors.New("an e-mail recipient is not an address"))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := smtpDial(ctx, "tcp", net.JoinHostPort(t.Host, strconv.Itoa(t.Port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	defer context.AfterFunc(ctx, func() { conn.Close() })()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	config := &tls.Config{ServerName: t.Host, MinVersion: tls.VersionTLS12, RootCAs: smtpRoots}
	if t.Security == "tls" {
		tc := tls.Client(conn, config)
		if err = tc.HandshakeContext(ctx); err != nil {
			return smtpError(err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, t.Host)
	if err != nil {
		return smtpError(err)
	}
	// Greet now, so a failed greeting is told apart from a missing extension, which Extension cannot do.
	if err = c.Hello(helloName(conn)); err != nil {
		return smtpError(err)
	}
	if t.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return permanent(errors.New("the SMTP server does not offer STARTTLS"))
		}
		if err = c.StartTLS(config); err != nil {
			return smtpError(err)
		}
	}
	if t.Username != "" {
		ok, mechanisms := c.Extension("AUTH")
		// Older servers say AUTH=LOGIN PLAIN, or AUTH=PLAIN LOGIN. shortcut: net/smtp matches the keyword's case as the
		// server wrote it, so a lower-case auth= line is missed; scan the raw reply if one turns up.
		for _, legacy := range []string{"LOGIN", "PLAIN"} {
			if found, more := c.Extension("AUTH=" + legacy); !ok && found {
				ok, mechanisms = true, legacy+" "+more
			}
		}
		var auth smtp.Auth
		switch {
		case !ok:
			return permanent(errors.New("the SMTP server does not offer AUTH"))
		case slices.Contains(strings.Fields(strings.ToUpper(mechanisms)), "PLAIN"):
			// The connection is TLS, which PlainAuth requires of a server other than localhost.
			auth = smtp.PlainAuth("", t.Username, t.Password, t.Host)
		case slices.Contains(strings.Fields(strings.ToUpper(mechanisms)), "LOGIN"):
			auth = loginAuth{t.Username, t.Password, new(int)}
		default:
			return permanent(errors.New("the SMTP server offers neither AUTH PLAIN nor AUTH LOGIN"))
		}
		if err = c.Auth(auth); err != nil {
			return smtpError(err)
		}
	}
	if err = c.Mail(from.Address); err != nil {
		return smtpError(err)
	}
	// A recipient the server refuses for good keeps no other from the message; a 4xx retries it for all.
	var refused error
	accepted := 0
	for _, a := range to {
		if err = smtpError(c.Rcpt(a.Address)); err == nil {
			accepted++
		} else if !isPermanent(err) {
			return err
		} else {
			refused = errors.Join(refused, fmt.Errorf("%s: %w", a.Address, err))
		}
	}
	if accepted == 0 {
		return refused
	}
	defer func() {
		if result == nil && refused != nil {
			result = partialError{refused}
		}
	}()
	w, err := c.Data()
	if err == nil {
		_, err = w.Write(emailMessage(job, from, to, time.Now()))
		err = errors.Join(err, w.Close())
	}
	if err != nil {
		return smtpError(err)
	}
	_ = c.Quit() // the server has taken the message
	return nil
}

// smtpError classifies err: a 4xx answer is retried, any other answer, a 5xx, is not, nor is a certificate that fails
// verification; a network failure is retried.
func smtpError(err error) error {
	var answer *textproto.Error
	if errors.As(err, &answer) {
		if answer.Code/100 == 4 {
			return fmt.Errorf("the SMTP server answered %w", err)
		}
		return permanent(fmt.Errorf("the SMTP server answered %w", err))
	}
	// A certificate that fails verification, or a server that does not speak TLS where the channel says it does (implicit
	// TLS on a STARTTLS port), is answered the same on every retry. A middlebox answering in plain text during an outage
	// is taken for the latter: a misconfigured channel is the likelier cause.
	// A login prompt that is not base64 is sent the same way every time, too.
	if errors.As(err, new(*tls.CertificateVerificationError)) || errors.As(err, new(tls.RecordHeaderError)) ||
		errors.As(err, new(base64.CorruptInputError)) {
		return permanent(err)
	}
	return err
}

// partialError is a message sent to some recipients only: the others were refused for good.
type partialError struct{ refused error }

func (e partialError) Error() string {
	return "sent, but a recipient was refused: " + e.refused.Error()
}

// helloName is the name to greet the server with: this host's name when it is a full one, else its address as a
// literal, as some servers refuse a bare name such as localhost.
func helloName(conn net.Conn) string {
	if name, err := os.Hostname(); err == nil && strings.Contains(name, ".") {
		return name
	}
	if a, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		if a.IP.To4() != nil {
			return "[" + a.IP.String() + "]"
		}
		return "[IPv6:" + a.IP.String() + "]"
	}
	return "localhost"
}

// loginAuth is AUTH LOGIN, for servers that offer no AUTH PLAIN. It runs over TLS only, as sendEmail ensures. It
// answers by step, the user name and then the password, since servers word their prompts as they please.
type loginAuth struct {
	user, password string
	step           *int
}

func (a loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }

func (a loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	*a.step++
	switch *a.step {
	case 1:
		return []byte(a.user), nil
	case 2:
		return []byte(a.password), nil
	}
	return nil, permanent(errors.New("the SMTP server asked AUTH LOGIN for more than a user name and a password"))
}

// emailMessage is the message as plain text: the subject as the subject, then the detail, the link (a full URL when the
// web UI's origin is known) and a footer. Every header is built from parsed addresses or encoded text, so none can hold
// a line break; the body is quoted-printable.
func emailMessage(job delivery.Job, from *mail.Address, to []*mail.Address, now time.Time) []byte {
	m := job.Message
	recipients := make([]string, len(to))
	for i, a := range to {
		recipients[i] = a.String()
	}
	id := strings.Map(func(r rune) rune {
		if r < 0x80 && (r == '-' || r == '_' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9') {
			return r
		}
		return '-'
	}, job.ID)
	var b bytes.Buffer
	domain := from.Address[strings.LastIndex(from.Address, "@")+1:]
	for _, h := range [][2]string{{"From", from.String()}, {"To", strings.Join(recipients, ",\r\n ")}, // folded: 20 names exceed a line
		{"Subject", mime.QEncoding.Encode("utf-8", cut(m.Subject, 200))}, {"Date", now.Format(time.RFC1123Z)},
		{"Message-ID", "<" + id + "@" + domain + ">"}, {"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"}, {"Content-Transfer-Encoding", "quoted-printable"}} {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	l := link(job)
	if l == "" {
		l = m.Link // a path in the web UI still says where to look
	}
	body := text(m)
	if l != "" {
		body += "\n\n" + l
	}
	q := quotedprintable.NewWriter(&b)
	_, _ = q.Write([]byte(body + "\n\n-- \nSent by Andaru\n"))
	_ = q.Close()
	return b.Bytes()
}
