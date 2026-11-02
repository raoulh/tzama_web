// Package sites publie les archives du site : releases de prod derrière un lien symbolique,
// previews par PR, index des previews.
package sites

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	releasesDir = "releases"
	previewsDir = "previews"
	prodLink    = "prod"
	// Zone de travail dans le même système de fichiers que les sites : les renommages vers
	// releases/ et previews/ y sont atomiques. Caddy ne sert pas la racine de /srv/sites.
	workDir = ".tmp"

	releasesFile = "releases.json"
	previewsFile = "previews.json"
	tagFile      = "deployd.tag"
	lockFile     = "deployd.lock"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidSHA indique si s est un sha de commit complet en minuscules.
func ValidSHA(s string) bool { return shaRe.MatchString(s) }

// ErrNotFound : release ou preview demandée absente.
var ErrNotFound = errors.New("introuvable")

type Config struct {
	SitesDir string
	StateDir string
	Keep     int
	Limits   Limits
	Location *time.Location
	Now      func() time.Time
	Logger   *slog.Logger
}

type Store struct {
	cfg Config
	mu  sync.Mutex
	// Points d'injection de panne pour les tests ; nil en production.
	failpoint func(stage string) error
}

func New(cfg Config) *Store {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if cfg.Keep < 1 {
		cfg.Keep = 1
	}
	return &Store{cfg: cfg}
}

func (s *Store) path(elem ...string) string {
	return filepath.Join(append([]string{s.cfg.SitesDir}, elem...)...)
}

func (s *Store) statePath(name string) string { return filepath.Join(s.cfg.StateDir, name) }

func (s *Store) fail(stage string) error {
	if s.failpoint == nil {
		return nil
	}
	return s.failpoint(stage)
}

// lock sérialise les opérations dans le processus (mutex) et avec `deployd rollback` lancé à
// côté dans le même conteneur (flock).
func (s *Store) lock() (func(), error) {
	s.mu.Lock()
	f, err := os.OpenFile(s.statePath(lockFile), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		s.mu.Unlock()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
		s.mu.Unlock()
	}, nil
}

// Init crée l'arborescence, vide la zone de travail (restes d'un arrêt brutal) et régénère
// l'index des previews.
func (s *Store) Init() error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	for _, d := range []string{releasesDir, previewsDir, workDir} {
		if err := os.MkdirAll(s.path(d), 0o755); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(s.path(workDir))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(s.path(workDir, e.Name())); err != nil {
			return err
		}
	}
	return s.writeIndex()
}

func randName(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (s *Store) newStage() (string, error) {
	dir, err := os.MkdirTemp(s.path(workDir), "stage-")
	if err != nil {
		return "", err
	}
	// MkdirTemp crée en 0700 : le dossier devient la racine publiée, Caddy doit pouvoir le lire.
	return dir, os.Chmod(dir, 0o755)
}

// discard retire un dossier publié en le sortant d'abord de l'arborescence servie : une
// suppression interrompue ne laisse jamais un site à moitié effacé en place.
func (s *Store) discard(path string) error {
	trash := s.path(workDir, randName("trash-"))
	if err := os.Rename(path, trash); err != nil {
		return err
	}
	return os.RemoveAll(trash)
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

type ProdResult struct {
	SHA      string `json:"sha"`
	Previous string `json:"previous,omitempty"`
	// Reused : la release existait déjà (même sha), l'archive n'a pas été réextraite.
	Reused bool `json:"reused,omitempty"`
}

// DeployProd extrait l'archive dans releases/<sha> puis fait pointer prod dessus. Tant que la
// bascule n'a pas eu lieu, prod reste sur la release précédente quelle que soit l'erreur.
func (s *Store) DeployProd(sha string, archive io.Reader) (ProdResult, error) {
	if !ValidSHA(sha) {
		return ProdResult{}, fmt.Errorf("sha invalide")
	}
	unlock, err := s.lock()
	if err != nil {
		return ProdResult{}, err
	}
	defer unlock()

	res := ProdResult{SHA: sha}
	res.Previous, _ = s.current()
	rel := s.path(releasesDir, sha)
	found, err := exists(rel)
	if err != nil {
		return res, err
	}
	if found {
		// Une release n'apparaît sous releases/ que par renommage d'une extraction complète.
		res.Reused = true
	} else {
		stage, err := s.newStage()
		if err != nil {
			return res, err
		}
		defer os.RemoveAll(stage)
		if err := extract(archive, stage, s.cfg.Limits); err != nil {
			return res, err
		}
		if err := s.fail("prod:extracted"); err != nil {
			return res, err
		}
		if err := os.Rename(stage, rel); err != nil {
			return res, err
		}
	}
	if err := s.fail("prod:before-switch"); err != nil {
		return res, err
	}
	if err := s.switchProd(sha); err != nil {
		return res, err
	}

	var st releasesState
	if err := readJSON(s.statePath(releasesFile), &st); err != nil {
		s.cfg.Logger.Error("historique des releases illisible, recréé", "err", err)
		st = releasesState{}
	}
	st.Releases = slices.DeleteFunc(st.Releases, func(e releaseEntry) bool { return e.SHA == sha })
	st.Releases = append(st.Releases, releaseEntry{SHA: sha, DeployedAt: s.cfg.Now().UTC()})
	if err := writeJSON(s.statePath(releasesFile), st); err != nil {
		s.cfg.Logger.Error("écriture de l'historique des releases", "err", err)
	}
	if err := s.prune(sha); err != nil {
		s.cfg.Logger.Error("purge des anciennes releases", "err", err)
	}
	return res, nil
}

// switchProd remplace atomiquement le lien prod. Le lien est relatif pour rester valable
// dans le conteneur Caddy, qui monte le même dossier ailleurs que l'hôte.
func (s *Store) switchProd(sha string) error {
	link := s.path(prodLink)
	tmp := s.path(workDir, randName("prod-link-"))
	if err := os.Symlink(filepath.Join(releasesDir, sha), tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	fi, err := os.Lstat(link)
	switch {
	case err == nil && fi.IsDir():
		// État initial : prod est un dossier vide créé à l'installation. rename(2) ne remplace
		// pas un dossier par un lien ; on ne retire le dossier que s'il est vide.
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("prod est un dossier non vide, à vider à la main: %w", err)
		}
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		return err
	}
	return syncDir(s.path())
}

// current renvoie le sha pointé par prod, "" si prod n'est pas encore un lien.
func (s *Store) current() (string, error) {
	target, err := os.Readlink(s.path(prodLink))
	if err != nil {
		return "", err
	}
	sha := filepath.Base(target)
	if filepath.Dir(target) != releasesDir || !ValidSHA(sha) {
		return "", fmt.Errorf("lien prod inattendu: %q", target)
	}
	return sha, nil
}

// Current renvoie le sha actuellement en prod.
func (s *Store) Current() (string, error) { return s.current() }

// ordered liste les releases présentes, de la plus ancienne à la plus récente selon
// l'historique ; celles absentes de l'historique passent en tête, triées par date.
func (s *Store) ordered() ([]string, error) {
	entries, err := os.ReadDir(s.path(releasesDir))
	if err != nil {
		return nil, err
	}
	var st releasesState
	if err := readJSON(s.statePath(releasesFile), &st); err != nil {
		st = releasesState{}
	}
	pos := map[string]int{}
	for i, e := range st.Releases {
		pos[e.SHA] = i
	}
	type rel struct {
		sha   string
		pos   int
		mtime time.Time
	}
	var rels []rel
	for _, e := range entries {
		if !e.IsDir() || !ValidSHA(e.Name()) {
			continue
		}
		r := rel{sha: e.Name(), pos: -1}
		if p, ok := pos[e.Name()]; ok {
			r.pos = p
		}
		if fi, err := e.Info(); err == nil {
			r.mtime = fi.ModTime()
		}
		rels = append(rels, r)
	}
	slices.SortStableFunc(rels, func(a, b rel) int {
		if a.pos != b.pos {
			return a.pos - b.pos
		}
		return a.mtime.Compare(b.mtime)
	})
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.sha
	}
	return out, nil
}

func (s *Store) prune(current string) error {
	rels, err := s.ordered()
	if err != nil {
		return err
	}
	if len(rels) <= s.cfg.Keep {
		return nil
	}
	keep := map[string]bool{current: true}
	for _, sha := range rels[len(rels)-s.cfg.Keep:] {
		keep[sha] = true
	}
	var errs []error
	removed := map[string]bool{}
	for _, sha := range rels {
		if keep[sha] {
			continue
		}
		if err := s.discard(s.path(releasesDir, sha)); err != nil {
			errs = append(errs, err)
			continue
		}
		removed[sha] = true
		s.cfg.Logger.Info("release supprimée", "sha", sha)
	}
	if len(removed) > 0 {
		var st releasesState
		if err := readJSON(s.statePath(releasesFile), &st); err == nil {
			st.Releases = slices.DeleteFunc(st.Releases, func(e releaseEntry) bool { return removed[e.SHA] })
			errs = append(errs, writeJSON(s.statePath(releasesFile), st))
		}
	}
	return errors.Join(errs...)
}

// Releases liste les releases présentes, de la plus ancienne à la plus récente.
func (s *Store) Releases() ([]string, error) { return s.ordered() }

// Rollback fait pointer prod sur sha (préfixe d'au moins 7 caractères accepté) ou, si sha est
// vide, sur la release déployée juste avant celle en prod. Renvoie l'ancien et le nouveau sha.
func (s *Store) Rollback(sha string) (from, to string, err error) {
	unlock, err := s.lock()
	if err != nil {
		return "", "", err
	}
	defer unlock()
	from, err = s.current()
	if err != nil {
		return "", "", fmt.Errorf("prod n'est pas encore déployée: %w", err)
	}
	rels, err := s.ordered()
	if err != nil {
		return from, "", err
	}
	if sha == "" {
		i := slices.Index(rels, from)
		if i <= 0 {
			return from, "", fmt.Errorf("%w: aucune release antérieure à %s", ErrNotFound, from)
		}
		to = rels[i-1]
	} else {
		if len(sha) < 7 || strings.Trim(sha, "0123456789abcdef") != "" {
			return from, "", fmt.Errorf("sha invalide %q", sha)
		}
		for _, r := range rels {
			if strings.HasPrefix(r, sha) {
				if to != "" {
					return from, "", fmt.Errorf("préfixe ambigu %q", sha)
				}
				to = r
			}
		}
		if to == "" {
			return from, "", fmt.Errorf("%w: release %s", ErrNotFound, sha)
		}
	}
	if err := s.switchProd(to); err != nil {
		return from, "", err
	}
	return from, to, nil
}

func previewName(pr int) string { return "pr-" + strconv.Itoa(pr) }

// DeployPreview remplace previews/pr-<N> par le contenu de l'archive. L'ancienne version reste
// en place tant que la nouvelle n'est pas entièrement extraite.
func (s *Store) DeployPreview(pr int, sha string, archive io.Reader) error {
	if pr <= 0 || !ValidSHA(sha) {
		return fmt.Errorf("pr ou sha invalide")
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()

	stage, err := s.newStage()
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := extract(archive, stage, s.cfg.Limits); err != nil {
		return err
	}
	if err := s.fail("preview:extracted"); err != nil {
		return err
	}
	dst := s.path(previewsDir, previewName(pr))
	found, err := exists(dst)
	if err != nil {
		return err
	}
	var old string
	if found {
		old = s.path(workDir, randName("old-"))
		if err := os.Rename(dst, old); err != nil {
			return err
		}
	}
	err = s.fail("preview:swapping")
	if err == nil {
		err = os.Rename(stage, dst)
	}
	if err != nil {
		if old != "" {
			if rerr := os.Rename(old, dst); rerr != nil {
				err = errors.Join(err, rerr)
			}
		}
		return err
	}
	if old != "" {
		if err := os.RemoveAll(old); err != nil {
			s.cfg.Logger.Error("suppression de l'ancienne preview", "pr", pr, "err", err)
		}
	}

	st := s.loadPreviews()
	st.Previews[pr] = previewEntry{SHA: sha, DeployedAt: s.cfg.Now().UTC()}
	if err := writeJSON(s.statePath(previewsFile), st); err != nil {
		s.cfg.Logger.Error("écriture de l'état des previews", "err", err)
	}
	return s.writeIndex()
}

// DeletePreview supprime previews/pr-<N> ; renvoie false si elle n'existait pas.
func (s *Store) DeletePreview(pr int) (bool, error) {
	if pr <= 0 {
		return false, fmt.Errorf("pr invalide")
	}
	unlock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	dst := s.path(previewsDir, previewName(pr))
	found, err := exists(dst)
	if err != nil {
		return false, err
	}
	if found {
		if err := s.discard(dst); err != nil {
			return false, err
		}
	}
	st := s.loadPreviews()
	if _, ok := st.Previews[pr]; ok {
		delete(st.Previews, pr)
		if err := writeJSON(s.statePath(previewsFile), st); err != nil {
			s.cfg.Logger.Error("écriture de l'état des previews", "err", err)
		}
	}
	return found, s.writeIndex()
}

func (s *Store) loadPreviews() previewsState {
	var st previewsState
	if err := readJSON(s.statePath(previewsFile), &st); err != nil {
		s.cfg.Logger.Error("état des previews illisible, recréé", "err", err)
		st = previewsState{}
	}
	if st.Previews == nil {
		st.Previews = map[int]previewEntry{}
	}
	return st
}

// SetServiceTag écrit le tag d'image demandé ; une unité systemd de l'hôte surveille ce
// fichier et fait la mise à jour (le service n'a pas accès à Docker).
func (s *Store) SetServiceTag(tag string) error {
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return writeFileAtomic(s.statePath(tagFile), []byte(tag+"\n"), 0o644)
}
