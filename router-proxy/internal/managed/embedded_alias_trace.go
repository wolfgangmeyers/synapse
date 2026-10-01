package managed

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ALIAS_TRACE_DIR is opt-in. It must name an existing, owner-owned 0700
// directory. No trace failure may enter the managed turn's error path.
// Expiry runs while the process lives, including after the byte cap. If the
// process exits first, expired files are pruned at the next trace startup.
const aliasTraceLimit = 256 * 1024
const aliasTraceAge = 24 * time.Hour
const aliasTraceFiles = 4
const aliasTracePrefix = "alias-trace-"

type aliasMarker struct{ phase, root, session, operation, call string }
type aliasTraceSink struct {
	queue chan aliasMarker
	done  chan struct{}
}

type aliasTraceOptions struct {
	now          func() time.Time
	expiry       <-chan time.Time // test-only clock; nil uses the production timer
	beforeCreate func()           // test-only directory swap seam
}

var aliasTraceOnce sync.Once
var aliasTraceGlobal *aliasTraceSink

func aliasTrace(phase, root, session, operation, call string) {
	dir := os.Getenv("ALIAS_TRACE_DIR")
	if dir == "" {
		return
	}
	aliasTraceOnce.Do(func() { aliasTraceGlobal = newAliasTraceSink(dir) })
	if aliasTraceGlobal == nil {
		return
	}
	select {
	case aliasTraceGlobal.queue <- aliasMarker{phase, root, session, operation, call}:
	default: // full or unavailable: tracing is best effort, never backpressure
	}
}

func newAliasTraceSink(dir string) *aliasTraceSink {
	return newAliasTraceSinkWithOptions(dir, aliasTraceOptions{})
}

func newAliasTraceSinkWithOptions(dir string, opts aliasTraceOptions) *aliasTraceSink {
	if !filepath.IsAbs(dir) {
		return nil
	}
	s := &aliasTraceSink{queue: make(chan aliasMarker, 128), done: make(chan struct{})}
	go s.run(dir, opts)
	return s
}

// The descriptor is the authority for every trace operation. Revalidate the
// configured pathname before each operation: a renamed directory is never
// replaced by an attacker-controlled pathname, and a detached one is rejected.
func aliasTraceDirectory(dir string) (int, bool) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, false
	}
	if !aliasTraceDirectoryValid(fd, dir) {
		_ = unix.Close(fd)
		return -1, false
	}
	return fd, true
}

func aliasTraceDirectoryValid(fd int, dir string) bool {
	var pinned, named unix.Stat_t
	if unix.Fstat(fd, &pinned) != nil || unix.Fstatat(unix.AT_FDCWD, dir, &named, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return false
	}
	return pinned.Mode&unix.S_IFMT == unix.S_IFDIR && pinned.Mode&0777 == 0700 &&
		pinned.Uid == uint32(os.Geteuid()) && pinned.Dev == named.Dev && pinned.Ino == named.Ino &&
		named.Mode&unix.S_IFMT == unix.S_IFDIR && named.Mode&0777 == 0700 && named.Uid == pinned.Uid
}

// Hash all identifiers, including call IDs: callers never need to decide if an
// identifier is actually a user-selected name or a private path.
func aliasTraceID(value string) string {
	if value == "" {
		return "-"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func aliasTraceLine(m aliasMarker) string {
	switch m.phase {
	case "startup", "checked-user", "provider-entry", "codex-terminal", "checked-assistant", "checked-intent", "worker-dispatch":
	default:
		return ""
	}
	return fmt.Sprintf("ALIAS_TRACE phase=%s root=%s session=%s operation=%s call=%s\n", m.phase, aliasTraceID(m.root), aliasTraceID(m.session), aliasTraceID(m.operation), aliasTraceID(m.call))
}

func (s *aliasTraceSink) run(dir string, opts aliasTraceOptions) {
	defer close(s.done)
	fd, ok := aliasTraceDirectory(dir)
	if !ok {
		return
	}
	defer unix.Close(fd)
	if opts.now == nil {
		opts.now = time.Now
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return
	}
	name := aliasTracePrefix + hex.EncodeToString(nonce[:]) + ".log"
	if opts.beforeCreate != nil {
		opts.beforeCreate()
	}
	if !aliasTraceDirectoryValid(fd, dir) {
		return
	}
	fileFD, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return
	}
	file := os.NewFile(uintptr(fileFD), name)
	defer file.Close()
	var created unix.Stat_t
	if unix.Fstat(fileFD, &created) != nil || created.Mode&unix.S_IFMT != unix.S_IFREG || created.Mode&0777 != 0600 || created.Uid != uint32(os.Geteuid()) {
		return
	}
	aliasTracePrune(fd, dir, name, opts.now())
	remaining := aliasTraceLimit
	expiry := opts.expiry
	if expiry == nil {
		timer := time.NewTimer(aliasTraceAge)
		defer timer.Stop()
		expiry = timer.C
	}
	writing := true
	for {
		select {
		case <-expiry:
			if aliasTraceDirectoryValid(fd, dir) && aliasTraceOwnedFile(fd, name, &created) {
				_ = unix.Unlinkat(fd, name, 0)
			}
			aliasTracePrune(fd, dir, "", opts.now())
			return
		case m, open := <-s.queue:
			if !open { // shutdown: close file and timer; next startup prunes it
				return
			}
			if !writing {
				continue // keep the expiry alive even after hitting the cap
			}
			line := aliasTraceLine(m)
			if line == "" {
				continue
			}
			if len(line) > remaining || !aliasTraceDirectoryValid(fd, dir) || !aliasTraceOwnedFile(fd, name, &created) {
				writing = false
				continue
			}
			n, err := file.WriteString(line)
			if err != nil || n != len(line) {
				writing = false
				continue
			}
			remaining -= n
			if remaining == 0 {
				writing = false
			}
		}
	}
}

func aliasTraceName(name string) bool {
	if !strings.HasPrefix(name, aliasTracePrefix) || !strings.HasSuffix(name, ".log") || len(name) != len(aliasTracePrefix)+32+len(".log") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimSuffix(strings.TrimPrefix(name, aliasTracePrefix), ".log"))
	return err == nil
}

func aliasTraceOwnedFile(fd int, name string, identity *unix.Stat_t) bool {
	if !aliasTraceName(name) {
		return false
	}
	var st unix.Stat_t
	if unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Uid != uint32(os.Geteuid()) {
		return false
	}
	return identity == nil || st.Dev == identity.Dev && st.Ino == identity.Ino
}

func aliasTracePrune(fd int, dir, current string, now time.Time) {
	if !aliasTraceDirectoryValid(fd, dir) {
		return
	}
	// Open a separate directory description so repeated pruning starts at entry 0.
	readFD, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	readDir := os.NewFile(uintptr(readFD), ".")
	entries, err := readDir.Readdirnames(-1)
	_ = readDir.Close()
	if err != nil {
		return
	}
	type traceFile struct {
		name string
		mod  time.Time
		size int64
	}
	var files []traceFile
	for _, name := range entries {
		if !aliasTraceName(name) || !aliasTraceDirectoryValid(fd, dir) || !aliasTraceOwnedFile(fd, name, nil) {
			continue
		}
		var st unix.Stat_t
		if unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
			continue
		}
		mod := time.Unix(st.Mtim.Sec, st.Mtim.Nsec)
		if now.Sub(mod) > aliasTraceAge {
			aliasTraceRemove(fd, dir, name, current)
			continue
		}
		files = append(files, traceFile{name, mod, st.Size})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mod.Equal(files[j].mod) {
			return files[i].name < files[j].name
		}
		return files[i].mod.After(files[j].mod)
	})
	var bytes int64
	kept := 0
	for _, f := range files {
		if f.name != current && (kept >= aliasTraceFiles || bytes+f.size > aliasTraceFiles*aliasTraceLimit) {
			aliasTraceRemove(fd, dir, f.name, current)
			continue
		}
		bytes += f.size
		kept++
	}
}

func aliasTraceRemove(fd int, dir, name, current string) {
	if name != current && aliasTraceDirectoryValid(fd, dir) && aliasTraceOwnedFile(fd, name, nil) {
		_ = unix.Unlinkat(fd, name, 0)
	}
}
