package sites

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type releaseEntry struct {
	SHA        string    `json:"sha"`
	DeployedAt time.Time `json:"deployed_at"`
}

type releasesState struct {
	// Ordre des déploiements, du plus ancien au plus récent ; un rollback ne le modifie pas.
	Releases []releaseEntry `json:"releases"`
}

type previewEntry struct {
	SHA        string    `json:"sha"`
	DeployedAt time.Time `json:"deployed_at"`
}

type previewsState struct {
	Previews map[int]previewEntry `json:"previews"`
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o644)
}

// writeFileAtomic écrit à côté puis renomme : un lecteur voit l'ancien ou le nouveau contenu,
// jamais un fichier à moitié écrit, même après une coupure.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
