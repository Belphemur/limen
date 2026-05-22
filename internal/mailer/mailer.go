// Package mailer wraps net/smtp for transactional email — currently
// only the Phase 9h signup verification email. The default
// implementation supports plain SMTP, STARTTLS, and implicit TLS;
// the choice is driven by config.MailerConfig.SMTP.TLS.
//
// The package does NOT depend on internal/signup so that the signup
// service can be tested with a stub mailer.
package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/smtp"
	"strings"
	texttemplate "text/template"
	"time"
)

// Mailer is the small dependency surface the signup service consumes.
type Mailer interface {
	// Send delivers a multipart/alternative message with both an HTML
	// and plain-text body to a single recipient.
	Send(ctx context.Context, to, subject, htmlBody, textBody string) error
}

// SignupVerifyTemplate is the embedded signup verification template
// pair. The HTML and text bodies are rendered from the same input
// struct; the resulting strings feed Mailer.Send.
//
// Templates are intentionally simple: no external assets, no remote
// images, no links other than the verification URL provided by the
// caller. Keeping every URL inside the caller's baseURL eliminates a
// whole class of open-redirect / phishing-bait risks.
type SignupVerifyTemplate struct {
	html *template.Template
	text *texttemplate.Template
}

//go:embed templates/*.tmpl
var signupTemplates embed.FS

// LoadSignupVerifyTemplate parses the embedded signup verify pair.
// Returns an error only if the embedded files fail to parse — which
// is a programming bug, not a runtime condition.
func LoadSignupVerifyTemplate() (*SignupVerifyTemplate, error) {
	htmlT, err := template.ParseFS(signupTemplates, "templates/signup_verify.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("mailer: parse signup verify html template: %w", err)
	}
	textT, err := texttemplate.ParseFS(signupTemplates, "templates/signup_verify.txt.tmpl")
	if err != nil {
		return nil, fmt.Errorf("mailer: parse signup verify text template: %w", err)
	}
	return &SignupVerifyTemplate{html: htmlT, text: textT}, nil
}

// SignupVerifyData feeds the verify-email template. VerifyURL must
// be the full URL the user clicks; the template does not append
// query parameters.
type SignupVerifyData struct {
	TenantName string
	OwnerName  string
	VerifyURL  string
	ExpiresIn  string
}

// Render returns (html, text, error) for the supplied data.
func (t *SignupVerifyTemplate) Render(data SignupVerifyData) (string, string, error) {
	var htmlBuf bytes.Buffer
	if err := t.html.Execute(&htmlBuf, data); err != nil {
		return "", "", fmt.Errorf("mailer: render signup verify html: %w", err)
	}
	var textBuf bytes.Buffer
	if err := t.text.Execute(&textBuf, data); err != nil {
		return "", "", fmt.Errorf("mailer: render signup verify text: %w", err)
	}
	return htmlBuf.String(), textBuf.String(), nil
}

// SMTPConfig matches config.SMTPConfig 1:1 — we copy rather than
// import to keep this package free of config-shape coupling.
type SMTPConfig struct {
	Host     string
	Port     int
	From     string
	Username string
	Password string
	// TLS: "none" | "starttls" | "tls".
	TLS string
}

// NewSMTPMailer returns a Mailer backed by net/smtp. Dial mode is
// chosen from cfg.TLS.
func NewSMTPMailer(cfg SMTPConfig) (Mailer, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("mailer: smtp host is required")
	}
	if cfg.Port <= 0 {
		return nil, errors.New("mailer: smtp port is required")
	}
	if strings.TrimSpace(cfg.From) == "" {
		return nil, errors.New("mailer: smtp from is required")
	}
	switch cfg.TLS {
	case "none", "starttls", "tls":
	case "":
		cfg.TLS = "starttls"
	default:
		return nil, fmt.Errorf("mailer: smtp tls %q is not one of none|starttls|tls", cfg.TLS)
	}
	return &smtpMailer{cfg: cfg}, nil
}

type smtpMailer struct {
	cfg SMTPConfig
}

func (m *smtpMailer) Send(ctx context.Context, to, subject, htmlBody, textBody string) error {
	if strings.TrimSpace(to) == "" {
		return errors.New("mailer: recipient is required")
	}
	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprintf("%d", m.cfg.Port))

	// Honour ctx cancellation via the dialer.
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mailer: dial smtp %s: %w", addr, err)
	}

	conn := net.Conn(netConn)
	if m.cfg.TLS == "tls" {
		tlsConn := tls.Client(netConn, &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = netConn.Close()
			return fmt.Errorf("mailer: tls handshake: %w", err)
		}
		conn = tlsConn
	}

	client, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mailer: smtp handshake: %w", err)
	}
	defer func() { _ = client.Close() }()

	if m.cfg.TLS == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("mailer: starttls: %w", err)
			}
		}
	}

	if m.cfg.Username != "" {
		auth := smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("mailer: auth: %w", err)
		}
	}

	if err := client.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("mailer: MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("mailer: RCPT TO: %w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("mailer: DATA: %w", err)
	}
	msg := buildMultipart(m.cfg.From, to, subject, htmlBody, textBody)
	if _, err := wc.Write([]byte(msg)); err != nil {
		_ = wc.Close()
		return fmt.Errorf("mailer: write message: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("mailer: close DATA: %w", err)
	}
	if err := client.Quit(); err != nil {
		// Some dev SMTP sinks close the connection mid-QUIT — treat
		// that as success since the message body is already accepted.
		return nil
	}
	return nil
}

// buildMultipart formats a minimal multipart/alternative RFC 5322
// message. The boundary is the fixed string "limen-mailer-boundary"
// — collisions in real content are vanishingly unlikely for the
// signup verify template, and using a constant keeps the function
// trivially testable.
func buildMultipart(from, to, subject, htmlBody, textBody string) string {
	const boundary = "limen-mailer-boundary-aa1f4c9d"
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n", boundary)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(textBody)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(htmlBody)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.String()
}

// NopMailer drops every message; useful in unit tests where mail is
// not the unit under test.
type NopMailer struct{}

// Send always succeeds without doing anything.
func (NopMailer) Send(context.Context, string, string, string, string) error { return nil }
