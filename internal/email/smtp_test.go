package email

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestSMTPSenderImplicitTLS(t *testing.T) {
	t.Parallel()
	certificate, roots := testCertificate(t)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	message := make(chan string, 1)
	go serveSMTP(t, listener, false, nil, message)

	sender, err := NewSMTPSender(SMTPConfig{
		Address: listener.Addr().String(), Username: "user", Password: "password",
		From: "ETH402 <verify@example.com>", TLSMode: SMTPModeTLS, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	sender.tlsConfig.RootCAs = roots
	sender.tlsConfig.ServerName = "127.0.0.1"
	if err := sender.Send(context.Background(), Message{
		To: "merchant@example.net", Subject: "Verify café",
		TextBody: "Use this one-time link: https://example.test/verify?token=secret",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	delivered := <-message
	for _, expected := range []string{
		"From: \"ETH402\" <verify@example.com>",
		"To: <merchant@example.net>",
		"Subject: =?UTF-8?q?Verify_caf=C3=A9?=",
		"Content-Transfer-Encoding: quoted-printable",
		"https://example.test/verify?token=3Dsecret",
	} {
		if !strings.Contains(delivered, expected) {
			t.Fatalf("message missing %q:\n%s", expected, delivered)
		}
	}
}

func TestSMTPSenderRequiresAdvertisedSTARTTLS(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go serveSMTP(t, listener, false, nil, make(chan string, 1))
	sender, err := NewSMTPSender(SMTPConfig{
		Address: listener.Addr().String(), From: "verify@example.com",
		TLSMode: SMTPModeSTARTTLS, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), Message{
		To: "merchant@example.net", Subject: "Verify", TextBody: "body",
	})
	if err == nil || !strings.Contains(err.Error(), "required STARTTLS") {
		t.Fatalf("error = %v, want required STARTTLS refusal", err)
	}
}

func TestSMTPSenderSTARTTLS(t *testing.T) {
	t.Parallel()
	certificate, roots := testCertificate(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	message := make(chan string, 1)
	go serveSMTP(t, listener, true, &certificate, message)
	sender, err := NewSMTPSender(SMTPConfig{
		Address: listener.Addr().String(), Username: "user", Password: "password",
		From: "verify@example.com", TLSMode: SMTPModeSTARTTLS, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	sender.tlsConfig.RootCAs = roots
	sender.tlsConfig.ServerName = "127.0.0.1"
	if err := sender.Send(context.Background(), Message{
		To: "merchant@example.net", Subject: "Verify", TextBody: "body",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if delivered := <-message; !strings.Contains(delivered, "\n\nbody") {
		t.Fatalf("unexpected message:\n%s", delivered)
	}
}

func TestSMTPSenderRejectsHeaderInjection(t *testing.T) {
	t.Parallel()
	sender, err := NewSMTPSender(SMTPConfig{
		Address: "smtp.example.com:465", From: "verify@example.com",
		TLSMode: SMTPModeTLS, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), Message{
		To: "merchant@example.net", Subject: "Verify\r\nBcc: victim@example.net", TextBody: "body",
	})
	if err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("error = %v, want subject rejection", err)
	}
}

func serveSMTP(t *testing.T, listener net.Listener, advertiseSTARTTLS bool, certificate *tls.Certificate, delivered chan<- string) {
	t.Helper()
	connection, err := listener.Accept()
	if err != nil {
		return
	}
	defer func() { _ = connection.Close() }()
	reader := textproto.NewReader(bufio.NewReader(connection))
	writer := textproto.NewWriter(bufio.NewWriter(connection))
	if err := writer.PrintfLine("220 test ESMTP"); err != nil {
		return
	}
	for {
		line, err := reader.ReadLine()
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.Fields(line)[0])
		switch command {
		case "EHLO", "HELO":
			if advertiseSTARTTLS {
				_ = writer.PrintfLine("250-test")
				_ = writer.PrintfLine("250-STARTTLS")
				_ = writer.PrintfLine("250 AUTH PLAIN")
			} else {
				_ = writer.PrintfLine("250-test")
				_ = writer.PrintfLine("250 AUTH PLAIN")
			}
		case "AUTH":
			_ = writer.PrintfLine("235 authenticated")
		case "MAIL", "RCPT":
			_ = writer.PrintfLine("250 accepted")
		case "DATA":
			_ = writer.PrintfLine("354 send data")
			body, readErr := io.ReadAll(reader.DotReader())
			if readErr != nil {
				return
			}
			delivered <- string(body)
			_ = writer.PrintfLine("250 queued")
		case "QUIT":
			_ = writer.PrintfLine("221 bye")
			return
		case "STARTTLS":
			if certificate == nil {
				_ = writer.PrintfLine("454 unavailable")
				continue
			}
			_ = writer.PrintfLine("220 ready")
			secure := tls.Server(connection, &tls.Config{
				Certificates: []tls.Certificate{*certificate}, MinVersion: tls.VersionTLS12,
			})
			if err := secure.Handshake(); err != nil {
				return
			}
			reader = textproto.NewReader(bufio.NewReader(secure))
			writer = textproto.NewWriter(bufio.NewWriter(secure))
			advertiseSTARTTLS = false
		default:
			_ = writer.PrintfLine("500 unsupported")
		}
	}
}

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return certificate, roots
}
