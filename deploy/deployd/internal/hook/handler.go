package hook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raoulh/tzama_web/deploy/deployd/internal/clientip"
	"github.com/raoulh/tzama_web/deploy/deployd/internal/sites"
)

var (
	prRe   = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	tagRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	tsRe   = regexp.MustCompile(`^[0-9]{1,12}$`)
	hashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var emptySHA256 = hex.EncodeToString(sha256.New().Sum(nil))

// Deployer est implémenté par *sites.Store.
type Deployer interface {
	DeployProd(sha string, archive io.Reader) (sites.ProdResult, error)
	DeployPreview(pr int, sha string, archive io.Reader) error
	DeletePreview(pr int) (bool, error)
	SetServiceTag(tag string) error
}

type Config struct {
	Secret   []byte
	MaxBytes int64
	TempDir  string
	// StatePath : fichier du dernier horodatage accepté par cible ; vide = en mémoire.
	StatePath string
	MaxSkew   time.Duration
	// UploadTime plafonne l'envoi ; il doit de toute façon finir dans la fenêtre MaxSkew.
	UploadTime    time.Duration
	MinRateBytes  int64
	MinRateWindow time.Duration
	TrustProxy    bool
	Now           func() time.Time
	Logger        *slog.Logger
}

type Handler struct {
	cfg    Config
	d      Deployer
	ledger *ledger
	// Un seul envoi à la fois : le disque et la bande passante de la VM sont modestes.
	upload chan struct{}

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
		cfg.UploadTime = cfg.MaxSkew
	}
	if cfg.MinRateBytes == 0 {
		cfg.MinRateBytes = 1 << 20
	}
	if cfg.MinRateWindow == 0 {
		cfg.MinRateWindow = 30 * time.Second
	}
	return &Handler{cfg: cfg, d: d, ledger: &ledger{path: cfg.StatePath},
		upload: make(chan struct{}, 1), seen: map[string]time.Time{}}
}

// httpError porte un message destiné au client ; les autres erreurs restent dans le journal.
type httpError struct {
	status int
	msg    string
	header map[string]string
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

// clip rend un en-tête non encore validé sûr à journaliser : court et sans caractère de contrôle.
func clip(s string) string {
	const max = 64
	var b strings.Builder
	for i, r := range s {
		if i >= max {
			b.WriteString("…")
			break
		}
		if r < 0x20 || r == 0x7f || r > 0x7e {
			r = '?'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// L'échéance de lecture n'est allongée qu'après vérification de la signature (dans
	// handle) : avant, le délai court du serveur s'applique. L'écriture couvre l'extraction.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(h.cfg.UploadTime + 5*time.Minute))

	start := time.Now()
	f := Fields{
		Timestamp:  r.Header.Get("X-Tzama-Timestamp"),
		Method:     r.Method,
		Target:     r.Header.Get("X-Tzama-Target"),
		PR:         r.Header.Get("X-Tzama-PR"),
		SHA:        r.Header.Get("X-Tzama-SHA"),
		Tag:        r.Header.Get("X-Tzama-Tag"),
		Length:     strconv.FormatInt(r.ContentLength, 10),
		BodySHA256: r.Header.Get("X-Tzama-Body-SHA256"),
	}
	log := h.cfg.Logger.With("method", clip(f.Method), "target", clip(f.Target), "pr", clip(f.PR),
		"sha", clip(f.SHA), "tag", clip(f.Tag), "ip", clip(clientip.From(r, h.cfg.TrustProxy)))
	result, n, err := h.handle(r, rc, f)
	if err != nil {
		status, msg := http.StatusInternalServerError, "erreur interne, voir le journal de deployd"
		var he *httpError
		var mbe *http.MaxBytesError
		var ne net.Error
		switch {
		case errors.As(err, &he):
			status, msg = he.status, he.msg
			for k, v := range he.header {
				w.Header().Set(k, v)
			}
		case errors.As(err, &mbe):
			status, msg = http.StatusRequestEntityTooLarge, "corps trop volumineux"
		case errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
			status, msg = http.StatusRequestTimeout, "envoi trop lent ou trop long"
		case errors.Is(err, sites.ErrUnsafeArchive):
			// Message issu du contenu de l'archive signée, sans chemin du serveur.
			status, msg = http.StatusBadRequest, err.Error()
		case errors.Is(err, sites.ErrNotFound):
			status, msg = http.StatusNotFound, "introuvable"
		}
		lvl := slog.LevelWarn
		if status >= 500 {
			lvl = slog.LevelError
		}
		log.Log(r.Context(), lvl, "déploiement refusé", "status", status, "bytes", n, "err", err.Error())
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	log.Info("déploiement effectué", "bytes", n, "duration_ms", time.Since(start).Milliseconds())
	result["ok"] = true
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) validate(r *http.Request, f Fields) (int, error) {
	if len(h.cfg.Secret) == 0 {
		return 0, fail(http.StatusServiceUnavailable, "HOOK_SECRET non configuré")
	}
	if r.ContentLength < 0 {
		return 0, fail(http.StatusLengthRequired, "Content-Length obligatoire")
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
	if !hashRe.MatchString(f.BodySHA256) {
		return 0, fail(http.StatusBadRequest, "X-Tzama-Body-SHA256 invalide (64 caractères hexadécimaux en minuscules)")
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
		return 0, fail(http.StatusBadRequest, "cible %q invalide pour %s", clip(f.Target), f.Method)
	}
	if spec.pr != (f.PR != "") || spec.tag != (f.Tag != "") ||
		(spec.sha && f.SHA == "") || (f.Target == "preview" && f.Method == "DELETE" && f.SHA != "") {
		return 0, fail(http.StatusBadRequest, "en-têtes attendus pour %s %s : %s", f.Method, f.Target, expected(f))
	}
	if needsBody(f) != (r.ContentLength > 0) {
		return 0, fail(http.StatusBadRequest, "corps inattendu ou manquant pour cette cible")
	}
	if !needsBody(f) && f.BodySHA256 != emptySHA256 {
		return 0, fail(http.StatusBadRequest, "X-Tzama-Body-SHA256 doit être l'empreinte du corps vide")
	}
	pr := 0
	if f.PR != "" {
		pr, _ = strconv.Atoi(f.PR)
	}
	return pr, nil
}

func needsBody(f Fields) bool { return f.Method == http.MethodPost && f.Target != "service" }

// ledgerKey : prod, service et chaque preview ont leur propre suite d'horodatages.
func ledgerKey(f Fields) string {
	if f.Target == "preview" {
		return "preview:" + f.PR
	}
	return f.Target
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

// handle vérifie et exécute ; renvoie le résultat JSON et la taille du corps reçu. Tout ce
// qui précède la lecture du corps ne dépend que des en-têtes : sans signature valide, aucun
// octet du corps n'est lu ni écrit sur disque.
func (h *Handler) handle(r *http.Request, rc *http.ResponseController, f Fields) (map[string]any, int64, error) {
	pr, err := h.validate(r, f)
	if err != nil {
		return nil, 0, err
	}
	ts, _ := strconv.ParseInt(f.Timestamp, 10, 64)
	signed := time.Unix(ts, 0)
	if !h.inWindow(signed) {
		return nil, 0, fail(http.StatusUnauthorized, "horodatage hors de la fenêtre de %s", h.cfg.MaxSkew)
	}
	sig, ok := parseSignature(r.Header.Get("X-Tzama-Signature"))
	if !ok {
		return nil, 0, fail(http.StatusUnauthorized, "X-Tzama-Signature absente ou mal formée")
	}
	if r.ContentLength > h.cfg.MaxBytes {
		return nil, 0, fail(http.StatusRequestEntityTooLarge, "corps supérieur à %d octets", h.cfg.MaxBytes)
	}
	if !hmac.Equal(Sign(h.cfg.Secret, f), sig) {
		return nil, 0, fail(http.StatusUnauthorized, "signature invalide")
	}

	if needsBody(f) {
		select {
		case h.upload <- struct{}{}:
			defer func() { <-h.upload }()
		default:
			return nil, 0, &httpError{status: http.StatusTooManyRequests, msg: "un envoi est déjà en cours",
				header: map[string]string{"Retry-After": "30"}}
		}
	}
	if !h.firstUse(hex.EncodeToString(sig), signed) {
		return nil, 0, fail(http.StatusConflict, "requête déjà reçue (rejeu)")
	}
	if ok, err := h.ledger.advance(ledgerKey(f), ts); err != nil {
		return nil, 0, fmt.Errorf("registre anti-rejeu: %w", err)
	} else if !ok {
		return nil, 0, fail(http.StatusConflict, "horodatage déjà utilisé ou plus ancien qu'une requête acceptée pour cette cible")
	}

	var tmp *os.File
	var n int64
	if needsBody(f) {
		tmp, n, err = h.receive(r, rc, f, signed)
		if tmp != nil {
			defer func() {
				tmp.Close()
				os.Remove(tmp.Name())
			}()
		}
		if err != nil {
			return nil, n, err
		}
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

// receive copie le corps sur disque en calculant son empreinte ; il n'est rendu (rembobiné)
// que si l'empreinte correspond à celle signée et si l'envoi a fini dans la fenêtre.
func (h *Handler) receive(r *http.Request, rc *http.ResponseController, f Fields, signed time.Time) (*os.File, int64, error) {
	tmp, err := os.CreateTemp(h.cfg.TempDir, "upload-")
	if err != nil {
		return nil, 0, err
	}
	// Les échéances de connexion sont en temps réel ; cfg.Now peut être une horloge de test.
	left := min(signed.Add(h.cfg.MaxSkew).Sub(h.cfg.Now()), h.cfg.UploadTime)
	hard := time.Now().Add(left)
	body := &minRateReader{r: http.MaxBytesReader(nil, r.Body, h.cfg.MaxBytes), set: rc.SetReadDeadline,
		now: time.Now, window: h.cfg.MinRateWindow, min: h.cfg.MinRateBytes, hard: hard}
	body.start()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), body)
	if err != nil {
		return tmp, n, fmt.Errorf("lecture du corps: %w", err)
	}
	if !hmac.Equal([]byte(hex.EncodeToString(sum.Sum(nil))), []byte(f.BodySHA256)) {
		return tmp, n, fail(http.StatusBadRequest, "le corps ne correspond pas à X-Tzama-Body-SHA256")
	}
	if h.cfg.Now().After(signed.Add(h.cfg.MaxSkew)) {
		return tmp, n, fail(http.StatusRequestTimeout, "envoi terminé après la fenêtre de %s suivant la signature", h.cfg.MaxSkew)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return tmp, n, err
	}
	return tmp, n, nil
}

func (h *Handler) inWindow(signed time.Time) bool {
	d := h.cfg.Now().Sub(signed)
	return d <= h.cfg.MaxSkew && d >= -h.cfg.MaxSkew
}

// firstUse retient les signatures vues tant qu'elles restent dans la fenêtre : jusqu'à
// max(signature, réception) + MaxSkew, pour couvrir un horodatage dans le futur.
func (h *Handler) firstUse(key string, signed time.Time) bool {
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
	h.seen[key] = maxTime(signed, now).Add(h.cfg.MaxSkew)
	return true
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
