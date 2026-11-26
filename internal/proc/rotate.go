package proc

import (
	"fmt"
	"io"
	"os"
)

// RotateFile moves the contents of path to path.1 (shifting path.1 to path.2
// and so on, keeping `keep` old files) once path is bigger than max bytes.
//
// It copies and then truncates instead of renaming: processes write their
// logs directly, with O_APPEND, so after the truncate they carry on at the
// start of the same file without being restarted. A line written between the
// copy and the truncate can be lost; that is the price of not routing every
// byte through the daemon. max <= 0 turns rotation off.
func RotateFile(path string, max int64, keep int) (bool, error) {
	if max <= 0 {
		return false, nil
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= max {
		return false, nil
	}
	if keep > 0 {
		for i := keep - 1; i >= 1; i-- {
			from, to := fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1)
			if _, err := os.Stat(from); err == nil {
				if err := os.Rename(from, to); err != nil {
					return false, err
				}
			}
		}
		if err := copyFile(path, path+".1", fi.Size()); err != nil {
			return false, err
		}
	}
	return true, os.Truncate(path, 0)
}

func copyFile(src, dst string, n int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(out, in, n); err != nil && err != io.EOF {
		out.Close()
		return err
	}
	return out.Close()
}

// RotateLogs rotates every process's logs that grew past its limit.
func (m *Manager) RotateLogs() {
	m.mu.Lock()
	ps := m.sortedLocked()
	m.mu.Unlock()
	for _, p := range ps {
		p.mu.Lock()
		max, keep := p.spec.logLimits()
		name, files := p.spec.Name, []string{p.outLog, p.errLog}
		p.mu.Unlock()
		for _, f := range files {
			rotated, err := RotateFile(f, max, keep)
			switch {
			case err != nil:
				m.logger.Printf("%s: rotating %s: %v", name, f, err)
			case rotated:
				m.logger.Printf("%s: rotated %s", name, f)
			}
		}
	}
}
