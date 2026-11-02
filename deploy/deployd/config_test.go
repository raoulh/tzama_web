package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raoulh/tzama_web/deploy/deployd/internal/contact"
)

func envOf(m map[string]string) envFunc { return func(k string) string { return m[k] } }

func TestConfigDefaults(t *testing.T) {
	c, err := loadConfig(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 8080 || c.SitesDir != "/srv/sites" || c.StateDir != "/state" || c.TrustProxy ||
		c.HookMaxBytes != 512<<20 || c.ReleasesKeep != 5 || c.ContactLimit != 5 || c.ContactWindow != 10*time.Minute {
		t.Fatalf("défauts %+v", c)
	}
	if c.SMTPEnabled || c.SMTP.Port != 587 || c.SMTP.TLS != contact.TLSStartTLS || c.SMTP.FromName != "Formulaire Tzama" ||
		c.SMTP.SkipVerify {
		t.Fatalf("SMTP %+v", c.SMTP)
	}
	if c.ContactOK != "/contact/merci/" || c.ContactError != "/contact/" || c.listenAddr() != ":8080" {
		t.Fatalf("chemins %q %q", c.ContactOK, c.ContactError)
	}
}

func TestConfigSMTP(t *testing.T) {
	base := map[string]string{"SMTP_HOST": "smtp.ecole.example", "SMTP_USERNAME": "formulaire",
		"SMTP_PASSWORD": " mot de passe ", "SMTP_FROM": "formulaire@ecole.example", "CONTACT_TO": "contact@ecole.example"}
	c, err := loadConfig(envOf(base))
	if err != nil {
		t.Fatal(err)
	}
	if !c.SMTPEnabled || c.SMTP.Username != "formulaire" || c.SMTP.Password != " mot de passe " {
		t.Fatalf("SMTP %+v", c.SMTP)
	}

	base["SMTP_PORT"] = "465"
	if c, _ := loadConfig(envOf(base)); c.SMTP.TLS != contact.TLSImplicit {
		t.Fatalf("port 465 : %q", c.SMTP.TLS)
	}
	base["SMTP_TLS"] = "starttls"
	base["SMTP_TLS_SKIP_VERIFY"] = "true"
	base["SMTP_FROM_NAME"] = "École Tzama"
	c, err = loadConfig(envOf(base))
	if err != nil || c.SMTP.TLS != contact.TLSStartTLS || !c.SMTP.SkipVerify || c.SMTP.FromName != "École Tzama" {
		t.Fatalf("%v %+v", err, c.SMTP)
	}
}

func TestConfigErrors(t *testing.T) {
	smtp := map[string]string{"SMTP_HOST": "smtp.ecole.example", "SMTP_FROM": "formulaire@ecole.example",
		"CONTACT_TO": "contact@ecole.example"}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"port", map[string]string{"PORT": "70000"}, "PORT"},
		{"port texte", map[string]string{"PORT": "abc"}, "PORT"},
		{"secret court", map[string]string{"HOOK_SECRET": "court"}, "HOOK_SECRET"},
		{"origine avec chemin", map[string]string{"ALLOWED_ORIGINS": "https://v2.tzama.fr/admin"}, "ALLOWED_ORIGINS"},
		{"origine sans schéma", map[string]string{"ALLOWED_ORIGINS": "v2.tzama.fr"}, "ALLOWED_ORIGINS"},
		{"redirection ouverte", map[string]string{"CONTACT_OK_PATH": "//evil.example/"}, "CONTACT_OK_PATH"},
		{"redirection absolue", map[string]string{"CONTACT_ERROR_PATH": "https://evil.example/"}, "CONTACT_ERROR_PATH"},
		{"durée", map[string]string{"CONTACT_RATE_WINDOW": "dix"}, "CONTACT_RATE_WINDOW"},
		{"booléen", map[string]string{"TRUST_PROXY": "peut-être"}, "TRUST_PROXY"},
		{"mode TLS", with(smtp, "SMTP_TLS", "ssl"), "SMTP_TLS"},
		{"skip-verify invalide", with(smtp, "SMTP_TLS_SKIP_VERIFY", "oui!"), "SMTP_TLS_SKIP_VERIFY"},
		{"skip-verify sans TLS", with(with(smtp, "SMTP_TLS", "none"), "SMTP_TLS_SKIP_VERIFY", "true"), "SMTP_TLS_SKIP_VERIFY"},
		{"nom d'affichage", with(smtp, "SMTP_FROM_NAME", "A\nBcc: x@evil.example"), "SMTP_FROM_NAME"},
		{"expéditeur absent", with(smtp, "SMTP_FROM", ""), "SMTP_FROM"},
		{"expéditeur avec nom", with(smtp, "SMTP_FROM", "Tzama <formulaire@ecole.example>"), "SMTP_FROM"},
		{"destinataire absent", with(smtp, "CONTACT_TO", ""), "CONTACT_TO"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadConfig(envOf(tc.env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("erreur %v, attendu %s", err, tc.want)
			}
		})
	}
}

func with(m map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for kk, vv := range m {
		if kk != k {
			out[kk] = vv
		}
	}
	return out
}

func TestMuxRoutes(t *testing.T) {
	cfg, err := loadConfig(envOf(map[string]string{"SITES_DIR": t.TempDir(), "STATE_DIR": t.TempDir()}))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	srv := httptest.NewServer(newMux(cfg, logger, newStore(cfg, logger), contact.NewLimiter(5, time.Minute, nil)))
	t.Cleanup(srv.Close)

	cases := []struct {
		method, path string
		status       int
	}{
		{"GET", "/healthz", 200},
		{"POST", "/hooks/deploy", 503}, // pas de secret
		{"DELETE", "/hooks/deploy", 503},
		{"GET", "/hooks/deploy", 405},
		{"GET", "/auth", 503}, // OAuth non configuré
		{"POST", "/api/contact", 415},
		{"GET", "/api/contact", 405},
		{"GET", "/", 404},
		{"GET", "/releases/", 404},
	}
	for _, tc := range cases {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Errorf("%s %s : %d, attendu %d (%s)", tc.method, tc.path, resp.StatusCode, tc.status, body)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s : en-têtes de sécurité absents", tc.method, tc.path)
		}
	}
}
