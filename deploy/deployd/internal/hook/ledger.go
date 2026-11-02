package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// ledger retient, par cible, l'horodatage de la dernière requête acceptée. Persisté, il
// survit aux redémarrages : une requête interceptée ne peut jamais repasser, même dans sa
// fenêtre de validité et même après un redémarrage du service.
type ledger struct {
	path string // vide : en mémoire seulement

	mu     sync.Mutex
	loaded bool
	last   map[string]int64
}

// advance accepte ts s'il dépasse strictement le dernier accepté pour key, et le persiste
// avant de rendre la main. ok=false : rejeu ou requête plus ancienne qu'une déjà acceptée.
func (l *ledger) advance(key string, ts int64) (ok bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded {
		if err := l.load(); err != nil {
			return false, err
		}
		l.loaded = true
	}
	if ts <= l.last[key] {
		return false, nil
	}
	prev, had := l.last[key]
	l.last[key] = ts
	if err := l.save(); err != nil {
		if had {
			l.last[key] = prev
		} else {
			delete(l.last, key)
		}
		return false, err
	}
	return true, nil
}

func (l *ledger) load() error {
	l.last = map[string]int64{}
	if l.path == "" {
		return nil
	}
	b, err := os.ReadFile(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// Fichier illisible : on refuse plutôt que de repartir de zéro, ce qui rouvrirait les rejeux.
	if err := json.Unmarshal(b, &l.last); err != nil {
		return fmt.Errorf("%s illisible: %w", l.path, err)
	}
	return nil
}

func (l *ledger) save() error {
	if l.path == "" {
		return nil
	}
	b, err := json.Marshal(l.last)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(l.path), ".hook-last-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), l.path)
}
