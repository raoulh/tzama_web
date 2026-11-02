package main

import (
	"archive/tar"
	"compress/gzip"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raoulh/tzama_web/deploy/deployd/internal/contact"
)

const testSecret = "0123456789abcdef0123456789abcdef-script"

func writeSite(t *testing.T, path, marker string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := "<h1>" + marker + "</h1>"
	for _, h := range []*tar.Header{
		{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "./index.html", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = tw.Write([]byte(body))
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// Le script client et le serveur doivent calculer la même signature : on les fait parler.
func TestDeployHookScript(t *testing.T) {
	for _, bin := range []string{"sh", "openssl", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s absent", bin)
		}
	}
	sitesDir, stateDir, work := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(sitesDir, "prod"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(envOf(map[string]string{"SITES_DIR": sitesDir, "STATE_DIR": stateDir, "HOOK_SECRET": testSecret}))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	store := newStore(cfg, logger)
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(newMux(cfg, logger, store, contact.NewLimiter(5, time.Minute, nil)))
	t.Cleanup(srv.Close)

	run := func(secret string, args ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{"scripts/deploy-hook.sh"}, args...)...)
		cmd.Env = append(os.Environ(), "DEPLOY_URL="+srv.URL+"/hooks/deploy", "HOOK_SECRET="+secret)
		out, err := cmd.CombinedOutput()
		if strings.Contains(string(out), secret) {
			t.Fatalf("secret affiché par le script : %s", out)
		}
		return string(out), err
	}

	a, b := filepath.Join(work, "a.tar.gz"), filepath.Join(work, "b.tar.gz")
	writeSite(t, a, "version A")
	writeSite(t, b, "version B")
	shaA, shaB := strings.Repeat("a", 40), strings.Repeat("b", 40)

	for _, args := range [][]string{{"prod", shaA, a}, {"prod", shaB, b}, {"preview", "12", shaB, b}} {
		if out, err := run(testSecret, args...); err != nil || !strings.Contains(out, `"ok":true`) {
			t.Fatalf("%v : %v\n%s", args, err, out)
		}
	}
	link, err := os.Readlink(filepath.Join(sitesDir, "prod"))
	if err != nil || link != "releases/"+shaB {
		t.Fatalf("prod -> %q (%v)", link, err)
	}
	if b, _ := os.ReadFile(filepath.Join(sitesDir, "previews", "pr-12", "index.html")); !strings.Contains(string(b), "version B") {
		t.Fatalf("preview : %q", b)
	}

	if out, err := run("mauvais-secret-mauvais-secret-mauvais", "prod", shaA, a); err == nil || !strings.Contains(out, "401") && !strings.Contains(out, "signature") {
		t.Fatalf("mauvais secret accepté : %v\n%s", err, out)
	}
	if out, err := run(testSecret, "delete-preview", "12"); err != nil {
		t.Fatalf("suppression : %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(sitesDir, "previews", "pr-12")); !os.IsNotExist(err) {
		t.Fatal("preview non supprimée")
	}
	if out, err := run(testSecret, "service", "v1.2.3"); err != nil {
		t.Fatalf("service : %v\n%s", err, out)
	}
	if tag, _ := os.ReadFile(filepath.Join(stateDir, "deployd.tag")); strings.TrimSpace(string(tag)) != "v1.2.3" {
		t.Fatalf("tag %q", tag)
	}
	if _, err := run(testSecret, "prod", shaA); err == nil {
		t.Fatal("usage invalide accepté")
	}
}

// Au-delà de 64 octets, HMAC hache d'abord la clé : le script doit faire de même. Les « % »
// et « \ » vérifient que la clé n'est jamais interprétée comme un format printf.
func TestDeployHookScriptLongSecret(t *testing.T) {
	for _, bin := range []string{"sh", "openssl", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s absent", bin)
		}
	}
	long := strings.Repeat(`6\%s-secret-long-`, 7)
	cfg, err := loadConfig(envOf(map[string]string{"SITES_DIR": t.TempDir(), "STATE_DIR": t.TempDir(), "HOOK_SECRET": long}))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	srv := httptest.NewServer(newMux(cfg, logger, newStore(cfg, logger), contact.NewLimiter(5, time.Minute, nil)))
	t.Cleanup(srv.Close)
	cmd := exec.Command("sh", "scripts/deploy-hook.sh", "service", "v2.0.0")
	cmd.Env = append(os.Environ(), "DEPLOY_URL="+srv.URL+"/hooks/deploy", "HOOK_SECRET="+long)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
}
