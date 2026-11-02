package sites

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type entry struct {
	name string
	typ  byte
	body string
	link string
	mode int64
}

func file(name, body string) entry { return entry{name: name, typ: tar.TypeReg, body: body} }
func dir(name string) entry        { return entry{name: name, typ: tar.TypeDir} }

func tarGz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
			if e.typ == tar.TypeDir {
				mode = 0o755
			}
		}
		size := int64(len(e.body))
		if e.typ != tar.TypeReg {
			size = 0
		}
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Linkname: e.link, Mode: mode, Size: size,
			ModTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Format: tar.FormatPAX}
		if e.typ == tar.TypeXGlobalHeader {
			hdr = &tar.Header{Name: e.name, Typeflag: e.typ, PAXRecords: map[string]string{"comment": "git"}}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// site : archive minimale valide, comme `tar czf - -C public .`.
func site(t *testing.T, marker string) []byte {
	return tarGz(t, dir("./"), file("./index.html", "<h1>"+marker+"</h1>"), dir("./css/"),
		file("./css/main.css", "body{}"))
}

func sha(c byte) string { return strings.Repeat(string(c), 40) }

func newTestStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	root := t.TempDir()
	sitesDir := filepath.Join(root, "sites")
	stateDir := filepath.Join(root, "state")
	for _, d := range []string{sitesDir, stateDir, filepath.Join(sitesDir, "prod"), filepath.Join(sitesDir, "previews")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s := New(Config{
		SitesDir: sitesDir, StateDir: stateDir, Keep: 5,
		Limits: Limits{MaxFiles: 1000, MaxBytes: 10 << 20},
		Now: func() time.Time {
			now = now.Add(time.Minute)
			return now
		},
	})
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	return s, sitesDir, stateDir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertEmptyDir(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("%s devrait être vide, contient %v", path, names)
	}
}
