//go:build embedded_private_status_probe

package managed

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// EnablePrivateStatusProbe is intentionally absent from ordinary builds. Call
// once, before the manager is exposed to any runner. The file must not exist.
func EnablePrivateStatusProbe(m *Manager, path string) error {
	if m == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) == "." {
		return errors.New("invalid private probe destination")
	}
	// Reject symlinks at every component, including ancestors of the private
	// directory. The immediate parent must be a private directory owned by us.
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid private probe directory")
		}
		if dir == filepath.Dir(path) {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0700 {
				return errors.New("private probe directory must be owned mode 0700")
			}
		}
		if dir == "/" {
			break
		}
	}
	dirfd, err := syscall.Open(filepath.Dir(path), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("cannot open private probe directory")
	}
	defer syscall.Close(dirfd)
	var dirStat syscall.Stat_t
	if err := syscall.Fstat(dirfd, &dirStat); err != nil || int(dirStat.Uid) != os.Geteuid() || dirStat.Mode&0777 != 0700 {
		return errors.New("private probe directory changed")
	}
	fd, err := unix.Openat(dirfd, filepath.Base(path), syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return errors.New("cannot create private probe file")
	}
	file := os.NewFile(uintptr(fd), "private-status-probe")
	var mu sync.Mutex
	failed := false
	previous := m.embeddedStatusObserver
	m.embeddedStatusObserver = func(trace embeddedTransportTrace) {
		if previous != nil {
			previous(trace)
		}
		// Do not serialize the trace itself: it may grow in the future. This
		// literal projection is the entire on-disk schema.
		row := struct {
			Ordinal        uint   `json:"ordinal"`
			Status         *int   `json:"status,omitempty"`
			TransportClass string `json:"transport_class,omitempty"`
		}{Ordinal: uint(trace.Ordinal)}
		if trace.Category != "" {
			switch trace.Category {
			case "dns", "tls_cert", "timeout", "connection":
				row.TransportClass = trace.Category
			default:
				row.TransportClass = "other"
			}
		} else {
			row.Status = &trace.Status
		}
		data, _ := json.Marshal(row)
		data = append(data, '\n')
		mu.Lock()
		defer mu.Unlock()
		if failed {
			return
		}
		if n, err := file.Write(data); err != nil || n != len(data) {
			failed = true
			_ = file.Close()
		}
	}
	return nil
}
