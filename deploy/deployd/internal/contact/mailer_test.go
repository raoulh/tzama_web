package contact

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP : serveur SMTP minimal qui enregistre l'enveloppe et le message reçus.
type fakeSMTP struct {
	addr     string
	startTLS bool // annonce STARTTLS
	implicit bool // TLS dès la connexion
	cert     tls.Certificate

	mu       sync.Mutex
	mailFrom []string
	rcptTo   []string
	data     []string
	authOK   bool
	authTLS  bool // l'authentification a eu lieu sous TLS
}

func newCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func (s *fakeSMTP) start(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
}

func (s *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	secure := false
	if s.implicit {
		c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}})
		secure = true
	}
	r := bufio.NewReader(c)
	say := func(l string) { _, _ = io.WriteString(c, l+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch {
		case verb == "EHLO":
			say("250-fake")
			if s.startTLS && !secure {
				say("250-STARTTLS")
			}
			say("250 AUTH PLAIN")
		case verb == "STARTTLS":
			say("220 go")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if tc.Handshake() != nil {
				return
			}
			c, r, secure = tc, bufio.NewReader(tc), true
		case verb == "AUTH":
			s.mu.Lock()
			s.authOK = strings.HasPrefix(line, "AUTH PLAIN ")
			s.authTLS = secure
			s.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM:"):
			s.mu.Lock()
			s.mailFrom = append(s.mailFrom, line[len("MAIL FROM:"):])
			s.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(strings.ToUpper(line), "RCPT TO:"):
			s.mu.Lock()
			s.rcptTo = append(s.rcptTo, line[len("RCPT TO:"):])
			s.mu.Unlock()
			say("250 ok")
		case verb == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.data = append(s.data, b.String())
			s.mu.Unlock()
			say("250 queued")
		case verb == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func mailerFor(s *fakeSMTP, pool *x509.CertPool, mode TLSMode) *SMTPMailer {
	host, port, _ := net.SplitHostPort(s.addr)
	p, _ := strconv.Atoi(port)
	return NewSMTPMailer(SMTPConfig{
		Host: host, Port: p, Username: "formulaire", Password: "mdp",
		From: "formulaire@ecole.example", FromName: "Formulaire Tzama", To: "contact@ecole.example",
		TLS: mode, Timeout: 5 * time.Second,
		TLSConfig: &tls.Config{RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS12},
	})
}

var parentMsg = Message{
	ReplyTo: &mail.Address{Name: "Zoé Lévêque", Address: "parent@famille.example"},
	Subject: "[Site] Demande de visite – Zoé Lévêque",
	Body:    "Bonjour,\nnous aimerions visiter l'école. Merci !\n",
}

func TestSMTPEnvelopeAndHeaders(t *testing.T) {
	for _, mode := range []TLSMode{TLSStartTLS, TLSImplicit} {
		t.Run(string(mode), func(t *testing.T) {
			cert, pool := newCert(t)
			srv := &fakeSMTP{cert: cert, startTLS: mode == TLSStartTLS, implicit: mode == TLSImplicit}
			srv.start(t)
			if err := mailerFor(srv, pool, mode).Send(context.Background(), parentMsg); err != nil {
				t.Fatal(err)
			}
			srv.mu.Lock()
			defer srv.mu.Unlock()

			// Enveloppe : toujours l'adresse autorisée, jamais celle du parent.
			if len(srv.mailFrom) != 1 || srv.mailFrom[0] != "<formulaire@ecole.example>" {
				t.Fatalf("MAIL FROM %v", srv.mailFrom)
			}
			if len(srv.rcptTo) != 1 || srv.rcptTo[0] != "<contact@ecole.example>" {
				t.Fatalf("RCPT TO %v", srv.rcptTo)
			}
			if !srv.authOK || !srv.authTLS {
				t.Fatalf("authentification : ok=%v sous TLS=%v", srv.authOK, srv.authTLS)
			}

			msg, err := mail.ReadMessage(strings.NewReader(srv.data[0]))
			if err != nil {
				t.Fatal(err)
			}
			from, err := mail.ParseAddress(msg.Header.Get("From"))
			if err != nil || from.Address != "formulaire@ecole.example" || from.Name != "Formulaire Tzama" {
				t.Fatalf("From %q (%v)", msg.Header.Get("From"), err)
			}
			replyTo, err := mail.ParseAddress(msg.Header.Get("Reply-To"))
			if err != nil || replyTo.Address != "parent@famille.example" || replyTo.Name != "Zoé Lévêque" {
				t.Fatalf("Reply-To %q (%v)", msg.Header.Get("Reply-To"), err)
			}
			if to := msg.Header.Get("To"); to != "<contact@ecole.example>" {
				t.Fatalf("To %q", to)
			}
			for _, k := range []string{"Sender", "Return-Path"} {
				if v := msg.Header.Get(k); v != "" {
					t.Fatalf("%s posé : %q", k, v)
				}
			}
			for _, k := range []string{"From", "To", "Sender", "Return-Path", "Message-Id"} {
				if strings.Contains(msg.Header.Get(k), "famille.example") {
					t.Fatalf("adresse du parent dans %s", k)
				}
			}
			subj, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
			if err != nil || subj != parentMsg.Subject {
				t.Fatalf("Subject %q -> %q (%v)", msg.Header.Get("Subject"), subj, err)
			}
			if !strings.HasPrefix(msg.Header.Get("Subject"), "=?utf-8?q?") {
				t.Fatalf("sujet non encodé RFC 2047 : %q", msg.Header.Get("Subject"))
			}
			if msg.Header.Get("Content-Transfer-Encoding") != "quoted-printable" ||
				msg.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
				t.Fatalf("en-têtes MIME %v", msg.Header)
			}
			if _, err := msg.Header.Date(); err != nil {
				t.Fatalf("Date : %v", err)
			}
			body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.ReplaceAll(parentMsg.Body, "\n", "\r\n") + "\r\n"; string(body) != want {
				t.Fatalf("corps %q, attendu %q", body, want)
			}
			if strings.Contains(srv.data[0], "\n") && strings.Count(srv.data[0], "\n") != strings.Count(srv.data[0], "\r\n") {
				t.Fatal("fin de ligne sans CR")
			}
		})
	}
}

func TestSMTPRequiresStartTLS(t *testing.T) {
	cert, pool := newCert(t)
	srv := &fakeSMTP{cert: cert, startTLS: false}
	srv.start(t)
	err := mailerFor(srv, pool, TLSStartTLS).Send(context.Background(), parentMsg)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("erreur %v", err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.authOK || len(srv.mailFrom) != 0 {
		t.Fatal("identifiants ou message envoyés en clair")
	}
}

func TestSMTPRejectsUntrustedCert(t *testing.T) {
	cert, _ := newCert(t)
	_, otherPool := newCert(t)
	srv := &fakeSMTP{cert: cert, startTLS: true}
	srv.start(t)
	if err := mailerFor(srv, otherPool, TLSStartTLS).Send(context.Background(), parentMsg); err == nil {
		t.Fatal("certificat inconnu accepté")
	}
}

// selfSignedCNOnly reproduit un certificat mailcow par défaut : auto-signé, nom d'hôte dans le
// CN seulement, sans SAN, ce que Go refuse même avec la bonne autorité.
func selfSignedCNOnly(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func TestSMTPSkipVerify(t *testing.T) {
	cert, pool := selfSignedCNOnly(t)
	for _, mode := range []TLSMode{TLSStartTLS, TLSImplicit} {
		t.Run(string(mode), func(t *testing.T) {
			srv := &fakeSMTP{cert: cert, startTLS: mode == TLSStartTLS, implicit: mode == TLSImplicit}
			srv.start(t)
			host, port, _ := net.SplitHostPort(srv.addr)
			p, _ := strconv.Atoi(port)
			cfg := SMTPConfig{Host: host, Port: p, Username: "formulaire", Password: "mdp",
				From: "formulaire@ecole.example", To: "contact@ecole.example", TLS: mode, Timeout: 5 * time.Second}

			// Vérification standard : refus, que l'autorité soit inconnue ou fournie (CN sans SAN).
			if err := NewSMTPMailer(cfg).Send(context.Background(), parentMsg); err == nil {
				t.Fatal("certificat auto-signé accepté sans SMTP_TLS_SKIP_VERIFY")
			}
			withCA := cfg
			withCA.TLSConfig = &tls.Config{RootCAs: pool, ServerName: host}
			if err := NewSMTPMailer(withCA).Send(context.Background(), parentMsg); err == nil {
				t.Fatal("certificat sans SAN accepté")
			}
			srv.mu.Lock()
			leaked := srv.authOK || len(srv.mailFrom) != 0
			srv.mu.Unlock()
			if leaked {
				t.Fatal("identifiants ou message envoyés malgré le refus du certificat")
			}

			cfg.SkipVerify = true
			if err := NewSMTPMailer(cfg).Send(context.Background(), parentMsg); err != nil {
				t.Fatalf("skip-verify : %v", err)
			}
			srv.mu.Lock()
			defer srv.mu.Unlock()
			if !srv.authTLS || len(srv.data) != 1 || srv.mailFrom[0] != "<formulaire@ecole.example>" {
				t.Fatalf("envoi : auth sous TLS=%v, messages %d, MAIL FROM %v", srv.authTLS, len(srv.data), srv.mailFrom)
			}
		})
	}
}

func TestSMTPSkipVerifyDoesNotAlterSharedConfig(t *testing.T) {
	shared := &tls.Config{ServerName: "x"}
	m := NewSMTPMailer(SMTPConfig{SkipVerify: true, TLSConfig: shared})
	if !m.tlsConfig().InsecureSkipVerify || shared.InsecureSkipVerify {
		t.Fatal("configuration TLS partagée modifiée")
	}
}

func TestBuildRejectsHeaderInjection(t *testing.T) {
	m := NewSMTPMailer(SMTPConfig{From: "formulaire@ecole.example", To: "contact@ecole.example"})
	for _, msg := range []Message{
		{ReplyTo: &mail.Address{Name: "A\r\nBcc: x@evil.example", Address: "a@b.example"}, Subject: "s"},
		{ReplyTo: &mail.Address{Address: "a@b.example"}, Subject: "s\nBcc: x@evil.example"},
	} {
		if _, err := m.build(msg, time.Now()); err == nil {
			t.Errorf("injection acceptée : %+v", msg)
		}
	}
	out, err := m.build(Message{ReplyTo: &mail.Address{Address: "a@b.example"}, Subject: "s",
		Body: strings.Repeat("é", 200)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(out), "\r\n") {
		if len(l) > 78 {
			t.Fatalf("ligne de %d octets", len(l))
		}
	}
}
