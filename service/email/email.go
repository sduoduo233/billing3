package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

var username, password, endpoint, port, tlsType, from string

func sendmail(to, subject, body string) error {
	slog.Info("sendmail", "to", to, "from", from, "subject", subject)

	var c *smtp.Client
	var err error
	switch tlsType {
	case "TLS":
		c, err = smtp.DialTLS(net.JoinHostPort(endpoint, port), &tls.Config{})
	case "STARTTLS":
		c, err = smtp.DialStartTLS(net.JoinHostPort(endpoint, port), &tls.Config{})
	case "NONE":
		c, err = smtp.Dial(net.JoinHostPort(endpoint, port))
	default:
		panic("unsupported tls type: " + tlsType)
	}
	if err != nil {
		return fmt.Errorf("send mail: dial: %w", err)
	}

	defer c.Close()

	saslClient := sasl.NewLoginClient(username, password)
	err = c.Auth(saslClient)
	if err != nil {
		return fmt.Errorf("send mail: auth: %w", err)
	}

	err = c.Mail(from, nil)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	err = c.Rcpt(to, nil)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}

	_, err = fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/html\r\n\r\n%s", from, to, subject, body)
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}

	err = w.Close()
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}

	err = c.Quit()
	if err != nil {
		return fmt.Errorf("send mail: %w", err)
	}

	return nil
}

// Init reads smtp settings from env variables and start background worker
func Init() {
	username = os.Getenv("SMTP_USERNAME")
	password = os.Getenv("SMTP_PASSWORD")
	endpoint = os.Getenv("SMTP_ENDPOINT")
	port = os.Getenv("SMTP_PORT")
	tlsType = os.Getenv("SMTP_TLS")
	from = os.Getenv("SMTP_FROM")

}

func SendMailAsync(ctx context.Context, to, subject, body string) error {
	go func() {
		err := sendmail(to, subject, body)
		if err != nil {
			slog.Error("send mail async", "error", err)
		}
	}()
	return nil
}
