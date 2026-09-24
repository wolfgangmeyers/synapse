package managed

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const stockPiSessionVersion = 3

type stockPiSessionHeader struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
}

type ownedLaunchSessionReservation struct {
	path   string
	device uint64
	inode  uint64
}

// materializeFreshStockPiSession creates Pi 0.73.1's exact v3 header before
// Process.Start. An explicit --session path is otherwise not written until the
// first response completes and cannot be resumed after a mid-response restart.
func materializeFreshStockPiSession(path, sessionID, cwd, timestamp string) (*ownedLaunchSessionReservation, error) {
	if !filepath.IsAbs(path) || sessionID == "" || cwd == "" || timestamp == "" {
		return nil, errors.New("managed stock Pi session header is invalid")
	}
	contents, err := json.Marshal(stockPiSessionHeader{Type: "session", Version: stockPiSessionVersion, ID: sessionID, Timestamp: timestamp, Cwd: cwd})
	if err != nil {
		return nil, err
	}
	contents = append(contents, '\n')
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		if err == nil {
			err = errors.New("managed stock Pi session root is not a directory")
		}
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	created := true
	defer func() {
		_ = file.Close()
		if created {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		if err == nil {
			err = errors.New("managed stock Pi session is not a private regular file")
		}
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("managed stock Pi session identity is unavailable")
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	created = false
	return &ownedLaunchSessionReservation{path: path, device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func (reservation *ownedLaunchSessionReservation) verify() error {
	if reservation == nil || reservation.path == "" {
		return errors.New("managed stock Pi session reservation is missing")
	}
	info, err := os.Lstat(reservation.path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		if err == nil {
			err = errors.New("managed stock Pi session is not a private regular file")
		}
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != reservation.device || uint64(stat.Ino) != reservation.inode {
		return errors.New("managed stock Pi session reservation identity changed")
	}
	return nil
}

// rollback removes only the exact inode created before Process.Start.
func (reservation *ownedLaunchSessionReservation) rollback() error {
	if reservation == nil || reservation.path == "" {
		return errors.New("managed stock Pi session rollback has no owner")
	}
	info, err := os.Lstat(reservation.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("managed stock Pi session identity changed")
		}
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != reservation.device || uint64(stat.Ino) != reservation.inode {
		return errors.New("managed stock Pi session rollback no longer owns the path")
	}
	return os.Remove(reservation.path)
}
