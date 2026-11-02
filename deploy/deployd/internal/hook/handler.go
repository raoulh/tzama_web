package hook

import (
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/raoulh/tzama_web/deploy/deployd/internal/clientip"
	"github.com/raoulh/tzama_web/deploy/deployd/internal/sites"
)

var (
	prRe  = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	tagRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	tsRe  = regexp.MustCompile(`^[0-9]{1,12}$`)
)

// Deployer est implémenté par *sites.Store.
type Deployer interface {
	DeployProd(sha string, archive io.Reader) (sites.ProdResult, error)
	DeployPreview(pr int, sha string, archive io.Reader) error
	DeletePreview(pr int) (bool, error)
	SetServiceTag(tag string) error
}

type Config struct {
	Secret     []byte
	MaxBytes   int64
	TempDir    string
	MaxSkew    time.Duration
	UploadTime time.Duration
	TrustProxy bool
	Now        func() time.Time
	Logger     *slog.Logger
}

type Handler struct {
	cfg Config
	d   Deployer

	mu   sync.Mutex
	seen map[string]time.Time
}

func New(cfg Config, d Deployer) *Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.MaxSkew == 0 {
		cfg.MaxSkew = 5 * time.Minute
	}
	if cfg.UploadTime == 0 {
		cfg.UploadTime = 30 * time.Minute
	}
	return &Handler{cfg: cfg, d: d, seen: map[string]time.Time{}}
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func fail(status int, format string, args ...any) error {
	return &httpError{status: status, msg: fmt.Sprintf(format, args...)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Seule route aux longs envois : on lève ici les délais courts du serveur.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(h.cfg.UploadTime))
	_ = rc.SetWriteDeadline(time.Now().Add(h.cfg.UploadTime + time.Minute))

	start := time.Now()
	f := Fields{
		Timestamp: r.Header.Get("X-Tzama-Timestamp"),
		Method:    r.Method,
		Target:    r.Header.Get("X-Tzama-Target"),
		PR:        r.Header.Get("X-Tzama-PR"),
		SHA:       r.Header.Get("X-Tzama-SHA"),
		Tag:       r.Header.Get("X-Tzama-Tag"),
	}
	log := h.cfg.Logger.With("method", f.Method, "target", f.Target, "pr", f.PR, "sha", f.SHA,
		"tag", f.Tag, "ip", clientip.From(r, h.cfg.TrustProxy))
	result, n, err := h.handle(r, f)
	if err != nil {
		status := http.StatusInternalServerError
		var he *httpError
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &he):
			status = he.status
		case errors.As(err, &mbe):
			status = http.StatusRequestEntityTooLarge
		case errors.Is(err, sites.ErrUnsafeArchive):
			status = http.StatusBadRequest
		case errors.Is(err, sites.ErrNotFound):
			status = http.StatusNotFound
		}
		lvl := slog.LevelWarn
		if status >= 500 {
			lvl = slog.LevelError
		}
		log.Log(r.Context(), lvl, "déploiement refusé", "status", status, "bytes", n, "err", err.Error())
		writeJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	log.Info("déploiement effectué", "bytes", n, "duration_ms", time.Since(start).Milliseconds())
	result["ok"] = true
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) validate(f Fields) (int, error) {
	if len(h.cfg.Secret) == 0 {
		return 0, fail(http.StatusServiceUnavailable, "HOOK_SECRET non configuré")
	}
	if !tsRe.MatchString(f.Timestamp) {
		return 0, fail(http.StatusBadRequest, "X-Tzama-Timestamp invalide")
	}
	if f.PR != "" && !prRe.MatchString(f.PR) {
		return 0, fail(http.StatusBadRequest, "X-Tzama-PR invalide")
	}
	if f.SHA != "" && !sites.ValidSHA(f.SHA) {
		return 0, fail(http.StatusBadRequest, "X-Tzama-SHA invalide (40 caractères hexadécimaux en minuscules)")
	}
	if f.Tag != "" && !tagRe.MatchString(f.Tag) {
		return 0, fail(http.StatusBadRequest, "X-Tzama-Tag invalide")
	}
	// Chaque cible n'accepte que ses champs : une requête signée n'a qu'une lecture possible.
	want := map[string]struct{ pr, sha, tag bool }{
		"POST prod":      {false, true, false},
		"POST preview":   {true, true, false},
		"POST service":   {false, false, true},
		"DELETE preview": {true, false, false},
	}
	spec, ok := want[f.Method+" "+f.Target]
	if !ok {
		return 0, fail(http.StatusBadRequest, "cible %q invalide pour %s", f.Target, f.Method)
	}
	if spec.pr != (f.PR != "") || spec.tag != (f.Tag != "") ||
		(spec.sha && f.SHA == "") || (f.Target == "preview" && f.Method == "DELETE" && f.SHA != "") {
		return 0, fail(http.StatusBadRequest, "en-têtes attendus pour %s %s : %s", f.Method, f.Target, expected(f))
	}
	pr := 0
	if f.PR != "" {
		pr, _ = strconv.Atoi(f.PR)
	}
	return pr, nil
}

func expected(f Fields) string {
	switch f.Method + " " + f.Target {
	case "POST prod":
		return "X-Tzama-SHA"
	case "POST preview":
		return "X-Tzama-PR, X-Tzama-SHA"
	case "POST service":
		return "X-Tzama-Tag (X-Tzama-SHA facultatif)"
	default:
		return "X-Tzama-PR"
	}
}

// handle vérifie et exécute ; renvoie le résultat JSON et la taille du corps reçu.
func (h *Handler) handle(r *http.Request, f Fields) (map[string]any, int64, error) {
	pr, err := h.validate(f)
	if err != nil {
		return nil, 0, err
	}
	ts, _ := strconv.ParseInt(f.Timestamp, 10, 64)
	now := h.cfg.Now()
	if d := now.Sub(time.Unix(ts, 0)); d > h.cfg.MaxSkew || d < -h.cfg.MaxSkew {
		return nil, 0, fail(http.StatusUnauthorized, "horodatage hors de la fenêtre de %s", h.cfg.MaxSkew)
	}
	sig, ok := parseSignature(r.Header.Get("X-Tzama-Signature"))
	if !ok {
		return nil, 0, fail(http.StatusUnauthorized, "X-Tzama-Signature absente ou mal formée")
	}
	if r.ContentLength > h.cfg.MaxBytes {
		return nil, 0, fail(http.StatusRequestEntityTooLarge, "corps supérieur à %d octets", h.cfg.MaxBytes)
	}

	// Le corps n'est lu qu'une fois : copié sur disque pendant le calcul du HMAC, il n'est
	// extrait qu'après vérification.
	tmp, err := os.CreateTemp(h.cfg.TempDir, "upload-")
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	mac := NewMAC(h.cfg.Secret, f)
	n, err := io.Copy(io.MultiWriter(tmp, mac), http.MaxBytesReader(nil, r.Body, h.cfg.MaxBytes))
	if err != nil {
		return nil, n, fmt.Errorf("lecture du corps: %w", err)
	}
	if !hmac.Equal(mac.Sum(nil), sig) {
		return nil, n, fail(http.StatusUnauthorized, "signature invalide")
	}
	if !h.firstUse(hex.EncodeToString(sig), time.Unix(ts, 0).Add(h.cfg.MaxSkew)) {
		return nil, n, fail(http.StatusConflict, "requête déjà reçue (rejeu)")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, n, err
	}
	needBody := f.Method == http.MethodPost && f.Target != "service"
	if needBody != (n > 0) {
		return nil, n, fail(http.StatusBadRequest, "corps inattendu ou manquant pour cette cible")
	}

	switch f.Method + " " + f.Target {
	case "POST prod":
		res, err := h.d.DeployProd(f.SHA, tmp)
		if err != nil {
			return nil, n, err
		}
		return map[string]any{"target": "prod", "sha": res.SHA, "previous": res.Previous, "reused": res.Reused}, n, nil
	case "POST preview":
		if err := h.d.DeployPreview(pr, f.SHA, tmp); err != nil {
			return nil, n, err
		}
		return map[string]any{"target": "preview", "pr": pr, "sha": f.SHA, "path": fmt.Sprintf("/pr-%d/", pr)}, n, nil
	case "DELETE preview":
		found, err := h.d.DeletePreview(pr)
		if err != nil {
			return nil, n, err
		}
		return map[string]any{"target": "preview", "pr": pr, "deleted": found}, n, nil
	default:
		if err := h.d.SetServiceTag(f.Tag); err != nil {
			return nil, n, err
		}
		return map[string]any{"target": "service", "tag": f.Tag}, n, nil
	}
}

// firstUse retient les signatures vues jusqu'à la fin de leur fenêtre de validité : une
// requête interceptée ne peut pas être rejouée, par exemple pour revenir à une ancienne prod.
func (h *Handler) firstUse(key string, expires time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.cfg.Now()
	for k, exp := range h.seen {
		if now.After(exp) {
			delete(h.seen, k)
		}
	}
	if _, ok := h.seen[key]; ok {
		return false
	}
	h.seen[key] = expires
	return true
}
