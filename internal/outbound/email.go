package outbound

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
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
// certificate verified against its host name. A 4xx answer and a network failure are retried; a 5xx answer, a failed
// verification and a server without STARTTLS are not.
func sendEmail(ctx context.Context, job delivery.Job) error {
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
	if t.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return permanent(errors.New("the SMTP server does not offer STARTTLS"))
		}
		if err = c.StartTLS(config); err != nil {
			return smtpError(err)
		}
	}
	if t.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return permanent(errors.New("the SMTP server does not offer AUTH"))
		}
		// The connection is TLS, which PlainAuth requires of a server other than localhost.
		if err = c.Auth(smtp.PlainAuth("", t.Username, t.Password, t.Host)); err != nil {
			return smtpError(err)
		}
	}
	if err = c.Mail(from.Address); err != nil {
		return smtpError(err)
	}
	for _, a := range to {
		if err = c.Rcpt(a.Address); err != nil {
			return smtpError(err)
		}
	}
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
	if errors.As(err, new(*tls.CertificateVerificationError)) {
		return permanent(err)
	}
	return err
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
	for _, h := range [][2]string{{"From", from.String()}, {"To", strings.Join(recipients, ", ")},
		{"Subject", mime.QEncoding.Encode("utf-8", cut(m.Subject, 200))}, {"Date", now.Format(time.RFC1123Z)},
		{"Message-ID", "<" + id + "@andaru>"}, {"MIME-Version", "1.0"},
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
