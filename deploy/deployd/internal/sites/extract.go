package sites

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Limits borne une archive : la taille compressée est bornée par le serveur HTTP, celle-ci
// protège contre les bombes de décompression et les archives à millions d'entrées.
type Limits struct {
	MaxFiles int
	MaxBytes int64
}

// ErrUnsafeArchive signale une archive refusée pour son contenu (et non une erreur d'E/S).
var ErrUnsafeArchive = errors.New("archive refusée")

func unsafeErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsafeArchive, fmt.Sprintf(format, args...))
}

// extract décompresse un tar.gz dans dst (dossier existant et vide). Seuls les fichiers
// réguliers et les dossiers sont acceptés ; aucun lien n'est créé, donc aucune entrée ne peut
// écrire hors de dst.
func extract(r io.Reader, dst string, lim Limits) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return unsafeErr("gzip invalide: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var files int
	var total int64
	hasIndex := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return unsafeErr("tar invalide: %v", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := entryName(hdr.Name)
		if err != nil {
			return err
		}
		files++
		if files > lim.MaxFiles {
			return unsafeErr("plus de %d entrées", lim.MaxFiles)
		}
		if name == "" {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := mkdirAll(dst, name); err != nil {
				return err
			}
		case tar.TypeReg:
			if hdr.Size < 0 || hdr.Size > lim.MaxBytes-total {
				return unsafeErr("taille décompressée supérieure à %d octets", lim.MaxBytes)
			}
			if dir := filepath.Dir(filepath.ToSlash(name)); dir != "." {
				if err := mkdirAll(dst, dir); err != nil {
					return err
				}
			}
			n, err := writeFile(target, tr, hdr)
			total += n
			if err != nil {
				return err
			}
			if name == "index.html" {
				hasIndex = true
			}
		default:
			return unsafeErr("type d'entrée interdit (%q) pour %q", hdr.Typeflag, hdr.Name)
		}
	}
	// Lire jusqu'au bout fait vérifier le CRC gzip : une archive tronquée ou corrompue après la
	// fin du tar est refusée plutôt que publiée.
	if _, err := io.Copy(io.Discard, io.LimitReader(gz, 1<<20)); err != nil {
		return unsafeErr("gzip invalide: %v", err)
	}
	if !hasIndex {
		return unsafeErr("index.html absent à la racine")
	}
	return nil
}

// entryName normalise un nom d'entrée ; "" désigne la racine de l'archive.
func entryName(raw string) (string, error) {
	name := raw
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}
	name = strings.TrimSuffix(name, "/")
	if name == "" || name == "." {
		return "", nil
	}
	if strings.Contains(name, `\`) || !fs.ValidPath(name) {
		return "", unsafeErr("chemin interdit %q", raw)
	}
	return name, nil
}

func mkdirAll(root, rel string) error {
	cur := root
	for part := range strings.SplitSeq(rel, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case err == nil && fi.IsDir():
			continue
		case err == nil:
			return unsafeErr("%q existe déjà et n'est pas un dossier", rel)
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
		if err := os.Mkdir(cur, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(cur, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(path string, r io.Reader, hdr *tar.Header) (int64, error) {
	// O_EXCL : une entrée en double est refusée plutôt que d'écraser silencieusement la première.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return 0, unsafeErr("entrée en double %q", hdr.Name)
		}
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(r, hdr.Size))
	if err == nil && n != hdr.Size {
		err = unsafeErr("entrée tronquée %q", hdr.Name)
	}
	if err == nil {
		err = f.Chmod(0o644)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	if !hdr.ModTime.IsZero() {
		_ = os.Chtimes(path, hdr.ModTime, hdr.ModTime)
	}
	return n, nil
}
