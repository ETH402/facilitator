package email

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/ETH402/facilitator/internal/secret"
)

func TestVerificationToken(t *testing.T) {
	t.Parallel()
	raw, hash, err := NewVerificationToken()
	if err != nil {
		t.Fatal(err)
	}
	if raw == hash || !secret.EqualHash(hash, raw) {
		t.Fatal("token hash is not verifiable")
	}
}

func TestLogSenderNeverLogsMessageBody(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	sender := LogSender{Logger: slog.New(slog.NewJSONHandler(&output, nil))}
	if err := sender.Send(context.Background(), Message{
		To: "developer@example.com", Subject: "verification",
		TextBody: "https://example.com/verify-email?token=raw-secret-token",
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "raw-secret-token") || strings.Contains(output.String(), "token=") {
		t.Fatalf("message body leaked to log: %s", output.String())
	}
}
