package email

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ETH402/facilitator/internal/secret"
)

type Message struct {
	To       string
	Subject  string
	TextBody string
}

type Sender interface {
	Send(context.Context, Message) error
}

type LogSender struct{ Logger *slog.Logger }

func (s LogSender) Send(_ context.Context, m Message) error {
	s.Logger.Info("development email delivery", "to", m.To, "subject", m.Subject, "body_redacted", true)
	return nil
}

type FileSender struct{ Directory string }

func (s FileSender) Send(_ context.Context, m Message) error {
	if err := os.MkdirAll(s.Directory, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("%d.json", time.Now().UTC().UnixNano())
	// #nosec G304 -- name is generated locally and contains no caller-controlled path.
	file, err := os.OpenFile(filepath.Join(s.Directory, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(m); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func NewVerificationToken() (raw, hash string, err error) {
	raw, err = secret.Token(32)
	if err != nil {
		return "", "", err
	}
	return raw, secret.Hash(raw), nil
}

func WriteMessage(w io.Writer, m Message) error { return json.NewEncoder(w).Encode(m) }
