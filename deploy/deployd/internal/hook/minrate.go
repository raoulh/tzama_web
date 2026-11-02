package hook

import (
	"io"
	"time"
)

// minRateReader impose un débit minimal par une échéance de lecture glissante : chaque
// fenêtre doit apporter au moins min octets, sinon l'échéance tombe et la lecture échoue.
// L'échéance ne dépasse jamais hard.
type minRateReader struct {
	r      io.Reader
	set    func(time.Time) error
	now    func() time.Time
	window time.Duration
	min    int64
	hard   time.Time
	got    int64
}

func (m *minRateReader) next() time.Time {
	d := m.now().Add(m.window)
	if d.After(m.hard) {
		return m.hard
	}
	return d
}

func (m *minRateReader) start() { _ = m.set(m.next()) }

func (m *minRateReader) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	m.got += int64(n)
	if m.got >= m.min {
		m.got = 0
		_ = m.set(m.next())
	}
	return n, err
}
