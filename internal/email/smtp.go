package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

const (
	SMTPModeSTARTTLS = "starttls"
	SMTPModeTLS      = "tls"
)

// SMTPConfig configures authenticated message submission. TLS is mandatory:
// STARTTLS must be advertised and complete successfully, or the connection is
// abandoned rather than silently downgrading to plaintext.
type SMTPConfig struct {
	Address  string
	Username string
	Password string
	From     string
	TLSMode  string
	Timeout  time.Duration
}

// SMTPSender is a provider-neutral production email sender. It intentionally
// supports only one recipient per message because merchant verification is the
// only delivery path and a wider surface would add header/envelope ambiguity.
type SMTPSender struct {
	address     string
	host        string
	username    string
	password    string
	from        *mail.Address
	tlsMode     string
	timeout     time.Duration
	tlsConfig   *tls.Config
	dialContext func(context.Context, string, string) (net.Conn, error)
	now         func() time.Time
}

func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	host, _, err := net.SplitHostPort(cfg.Address)
	if err != nil || host == "" {
		return nil, errors.New("SMTP address must be host:port")
	}
	if cfg.TLSMode != SMTPModeSTARTTLS && cfg.TLSMode != SMTPModeTLS {
		return nil, errors.New("SMTP TLS mode must be starttls or tls")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("SMTP timeout must be positive")
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, errors.New("SMTP username and password must be configured together")
	}
	from, err := parseMailbox(cfg.From)
	if err != nil {
		return nil, errors.New("SMTP from address is invalid")
	}
	dialer := &net.Dialer{Timeout: cfg.Timeout}
	return &SMTPSender{
		address: cfg.Address, host: host, username: cfg.Username,
		password: cfg.Password, from: from, tlsMode: cfg.TLSMode,
		timeout: cfg.Timeout,
		tlsConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: host,
		},
		dialContext: dialer.DialContext,
		now:         time.Now,
	}, nil
}

func (s *SMTPSender) Send(ctx context.Context, message Message) error {
	to, err := parseMailbox(message.To)
	if err != nil {
		return errors.New("email recipient is invalid")
	}
	encoded, err := s.encode(message, to)
	if err != nil {
		return err
	}
	client, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if err := client.Mail(s.from.Address); err != nil {
		return smtpError("sender", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return smtpError("recipient", err)
	}
	writer, err := client.Data()
	if err != nil {
		return smtpError("data", err)
	}
	if _, err := writer.Write(encoded); err != nil {
		_ = writer.Close()
		return smtpError("body", err)
	}
	if err := writer.Close(); err != nil {
		return smtpError("delivery", err)
	}
	if err := client.Quit(); err != nil {
		return smtpError("quit", err)
	}
	return nil
}

// Probe verifies connectivity, certificate identity, mandatory TLS, and
// authentication without sending a message.
func (s *SMTPSender) Probe(ctx context.Context) error {
	client, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if err := client.Noop(); err != nil {
		return smtpError("probe", err)
	}
	if err := client.Quit(); err != nil {
		return smtpError("quit", err)
	}
	return nil
}

func (s *SMTPSender) open(ctx context.Context) (*smtp.Client, error) {
	deadline := s.now().Add(s.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	connection, err := s.dialContext(ctx, "tcp", s.address)
	if err != nil {
		return nil, smtpError("connect", err)
	}
	release := true
	defer func() {
		if release {
			_ = connection.Close()
		}
	}()
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, errors.New("SMTP deadline setup failed")
	}

	var client *smtp.Client
	switch s.tlsMode {
	case SMTPModeTLS:
		secure := tls.Client(connection, s.tlsConfig.Clone())
		if err := secure.HandshakeContext(ctx); err != nil {
			return nil, smtpError("TLS handshake", err)
		}
		client, err = smtp.NewClient(secure, s.host)
	case SMTPModeSTARTTLS:
		client, err = smtp.NewClient(connection, s.host)
		if err == nil {
			supported, _ := client.Extension("STARTTLS")
			if !supported {
				_ = client.Close()
				return nil, errors.New("SMTP server does not advertise required STARTTLS")
			}
			err = client.StartTLS(s.tlsConfig.Clone())
		}
	default:
		// Construction rejects this; keep the send path fail-closed if a future
		// caller creates the value without the constructor.
		return nil, errors.New("SMTP TLS mode is invalid")
	}
	if err != nil {
		return nil, smtpError("session setup", err)
	}

	state, secure := client.TLSConnectionState()
	if !secure || !state.HandshakeComplete {
		_ = client.Close()
		return nil, errors.New("SMTP session is not protected by TLS")
	}
	if s.username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
			return nil, smtpError("authentication", err)
		}
	}
	release = false
	return client, nil
}

func (s *SMTPSender) encode(message Message, to *mail.Address) ([]byte, error) {
	if strings.ContainsAny(message.Subject, "\r\n") {
		return nil, errors.New("email subject contains a line break")
	}
	var output bytes.Buffer
	headers := []string{
		"Date: " + s.now().UTC().Format(time.RFC1123Z),
		"From: " + s.from.String(),
		"To: " + to.String(),
		"Subject: " + mime.QEncoding.Encode("UTF-8", message.Subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: quoted-printable",
	}
	for _, header := range headers {
		if strings.ContainsAny(header, "\r\n") {
			return nil, errors.New("email header contains a line break")
		}
		output.WriteString(header)
		output.WriteString("\r\n")
	}
	output.WriteString("\r\n")
	body := quotedprintable.NewWriter(&output)
	if _, err := body.Write([]byte(message.TextBody)); err != nil {
		return nil, errors.New("encode email body")
	}
	if err := body.Close(); err != nil {
		return nil, errors.New("finish email body")
	}
	return output.Bytes(), nil
}

func parseMailbox(value string) (*mail.Address, error) {
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return nil, errors.New("invalid mailbox")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address == "" {
		return nil, errors.New("invalid mailbox")
	}
	return address, nil
}

// smtpError deliberately omits the server's response text. SMTP servers often
// echo the rejected recipient address, and callers log delivery errors; keeping
// only the stage and numeric reply code prevents unredacted merchant email from
// entering logs while retaining enough information to distinguish 4xx and 5xx.
func smtpError(stage string, err error) error {
	var protocol *textproto.Error
	if errors.As(err, &protocol) {
		return fmt.Errorf("SMTP %s failed with status %d", stage, protocol.Code)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("SMTP %s canceled", stage)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("SMTP %s timed out", stage)
	}
	return fmt.Errorf("SMTP %s failed", stage)
}
