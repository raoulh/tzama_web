package contact

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Message : seul le contenu varie d'un envoi à l'autre ; expéditeur et destinataire sont fixés
// par la configuration du Mailer.
type Message struct {
	ReplyTo *mail.Address
	Subject string
	Body    string
}

type Mailer interface {
	Send(ctx context.Context, m Message) error
}

type TLSMode string

const (
	TLSStartTLS TLSMode = "starttls"
	TLSImplicit TLSMode = "tls"
	TLSNone     TLSMode = "none"
)

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	// From : adresse autorisée (SPF/DKIM) pour le serveur d'envoi. Elle sert d'enveloppe et
	// d'en-tête From ; l'adresse du parent ne va que dans Reply-To.
	From     string
	FromName string
	To       string
	TLS      TLSMode
	Timeout  time.Duration
	// SkipVerify garde le chiffrement mais accepte n'importe quel certificat : réservé à un
	// serveur interne au certificat auto-signé, joint par un réseau de confiance.
	SkipVerify bool
	// TLSConfig : nil en production ; les tests y mettent leur autorité.
	TLSConfig *tls.Config
}

type SMTPMailer struct {
	cfg SMTPConfig
}

func NewSMTPMailer(cfg SMTPConfig) *SMTPMailer {
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	return &SMTPMailer{cfg: cfg}
}

func (s *SMTPMailer) tlsConfig() *tls.Config {
	cfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	if s.cfg.TLSConfig != nil {
		cfg = s.cfg.TLSConfig.Clone()
	}
	cfg.InsecureSkipVerify = s.cfg.SkipVerify
	return cfg
}

func (s *SMTPMailer) Send(ctx context.Context, m Message) error {
	data, err := s.build(m, time.Now())
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	d := net.Dialer{}
	var conn net.Conn
	if s.cfg.TLS == TLSImplicit {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: s.tlsConfig()}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connexion smtp: %w", err)
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}
	if s.cfg.TLS == TLSStartTLS {
		// Pas de repli en clair : un serveur (ou un intermédiaire) qui n'annonce pas STARTTLS
		// ferait sinon circuler identifiants et message en clair.
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp: le serveur n'annonce pas STARTTLS")
		}
		if err := c.StartTLS(s.tlsConfig()); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(s.cfg.To); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		w.Close()
		return fmt.Errorf("smtp data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	return c.Quit()
}

// build produit le message RFC 5322 : en-têtes non ASCII en RFC 2047, corps UTF-8 en
// quoted-printable, fins de ligne CRLF.
func (s *SMTPMailer) build(m Message, now time.Time) ([]byte, error) {
	for _, v := range []string{m.Subject, m.ReplyTo.Address, m.ReplyTo.Name} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, fmt.Errorf("retour à la ligne interdit dans un en-tête")
		}
	}
	from := mail.Address{Name: s.cfg.FromName, Address: s.cfg.From}
	domain := s.cfg.From[strings.LastIndexByte(s.cfg.From, '@')+1:]
	id := make([]byte, 12)
	_, _ = rand.Read(id)

	var b bytes.Buffer
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	hdr("From", from.String())
	hdr("To", (&mail.Address{Address: s.cfg.To}).String())
	hdr("Reply-To", m.ReplyTo.String())
	hdr("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	hdr("Date", now.Format(time.RFC1123Z))
	hdr("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", "text/plain; charset=utf-8")
	hdr("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	body := strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	b.WriteString("\r\n")
	return b.Bytes(), nil
}
