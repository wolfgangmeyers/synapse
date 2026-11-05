package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maxCodexCredentialBytes   = 64 << 10
	openCredentialLockTimeout = 5 * time.Second
)

// CodexCredential is the router-owned, request-scoped SDK authentication record.
// It is stored unencrypted in an owner-only local file; callers must not log it.
type CodexCredential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	AccountID    string    `json:"account_id,omitempty"`
}

type LocalCodexCredentialStore struct {
	path string
	// Optional ordinary-Pi compatibility consumer. Configure before sharing the
	// store. It runs under the credential lock after the authoritative write.
	projection func(context.Context, *CodexCredential) error
}

func (s *LocalCodexCredentialStore) SetProjection(projection func(context.Context, *CodexCredential) error) {
	s.projection = projection
}

func (s *LocalCodexCredentialStore) project(ctx context.Context, credential *CodexCredential) error {
	if s.projection == nil {
		return nil
	}
	if err := s.projection(ctx, credential); err != nil {
		return errors.New("credential consumer synchronization failed")
	}
	return nil
}

// Directory identifies the protected credential tree without reading tokens.
// Project admission must exclude this parent, including lock and temp files.
func (s *LocalCodexCredentialStore) Directory() string { return filepath.Dir(s.path) }

// OpenLocalCodexCredentialStore checks the existing path without reading tokens.
// Each operation reopens and rechecks every component to avoid trusting this check.
func OpenLocalCodexCredentialStore(path string) (*LocalCodexCredentialStore, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.HasSuffix(path, "/") {
		return nil, errors.New("credential path must be a clean absolute file path")
	}
	s := &LocalCodexCredentialStore{path: path}
	dir, name, err := s.openDir(true)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(dir)
	if err = checkLeafIfExists(dir, name); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), openCredentialLockTimeout)
	defer cancel()
	lock, err := lockLeaf(dir, name+".lock", ctx)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(lock)
	if err = cleanupTemps(dir, name); err != nil {
		return nil, err
	}
	return s, nil
}

// openDir walks from / without following symlinks, checking each ancestor
// and requiring a private 0700 destination directory.
func (s *LocalCodexCredentialStore) openDir(create bool) (int, string, error) {
	parts := strings.Split(strings.TrimPrefix(s.path, "/"), "/")
	name := parts[len(parts)-1]
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, "", errors.New("credential root unavailable")
	}
	private := false
	ancestors := parts[:len(parts)-1]
	if err = checkDir(fd, &private, len(ancestors) == 0); err != nil {
		syscall.Close(fd)
		return -1, "", err
	}
	for i, part := range ancestors {
		if part == "" || part == "." || part == ".." {
			syscall.Close(fd)
			return -1, "", errors.New("invalid credential directory")
		}
		next, e := unix.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		if e == syscall.ENOENT && create {
			if e = unix.Mkdirat(fd, part, 0o700); e != nil && e != syscall.EEXIST {
				syscall.Close(fd)
				return -1, "", fmt.Errorf("create credential directory: %w", e)
			}
			if e = syscall.Fsync(fd); e != nil {
				syscall.Close(fd)
				return -1, "", errors.New("sync credential parent directory failed")
			}
			next, e = unix.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
		}
		syscall.Close(fd)
		if e != nil {
			return -1, "", fmt.Errorf("open credential directory: %w", e)
		}
		fd = next
		isDest := (i == len(ancestors)-1)
		if e = checkDir(fd, &private, isDest); e != nil {
			syscall.Close(fd)
			return -1, "", e
		}
	}
	if !private {
		syscall.Close(fd)
		return -1, "", errors.New("credential destination must be owner-only")
	}
	return fd, name, nil
}

// Single-user developer workstation requirement: DO NOT revert or tighten ancestor permission checks to require 0700.
// On developer workstations (such as macOS home directories), user-owned ancestors frequently have standard 0750 or 0755 permissions.
// These are single-user machines, not multi-user shared hosts. Ancestors owned by the user are safe as long as group and others lack write
// bits (mode&0o022 == 0). Only the private leaf directory destination (e.g. .mecha-developer) must be strictly 0700.
func checkDir(fd int, private *bool, isDest bool) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return errors.New("credential parent is not a directory")
	}
	mode := st.Mode & 0o7777
	if isDest {
		if st.Uid != uint32(os.Geteuid()) || mode != 0o700 {
			return errors.New("credential destination must be owner-only")
		}
		*private = true
		return nil
	}
	if st.Uid == uint32(os.Geteuid()) && os.Geteuid() != 0 {
		if mode == 0o700 {
			*private = true
		} else if mode&0o022 != 0 {
			return errors.New("credential ancestor is writable")
		}
	} else if st.Uid == 0 && !*private {
		// Root-owned sticky temporary directories are safe against replacement by
		// other users and allow private t.TempDir() trees under /tmp.
		if mode&0o022 != 0 && mode != 0o1777 {
			return errors.New("credential ancestor is writable")
		}
	} else if st.Uid == uint32(os.Geteuid()) && mode == 0o700 {
		*private = true
	} else {
		return errors.New("credential parent has unsafe ownership")
	}
	return nil
}

func checkFile(fd int) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&0o7777 != 0o600 || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) {
		return errors.New("credential file has unsafe type, ownership, mode or link count")
	}
	return nil
}

func openLeaf(dir int, name string) (int, error) {
	fd, err := unix.Openat(dir, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return -1, err
	}
	if err = checkFile(fd); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}

func checkLeafIfExists(dir int, name string) error {
	fd, err := openLeaf(dir, name)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	return syscall.Close(fd)
}

func lockLeaf(dir int, name string, ctx context.Context) (int, error) {
	fd, err := unix.Openat(dir, name, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CREAT|syscall.O_EXCL, 0o600)
	if errors.Is(err, syscall.EEXIST) {
		fd, err = unix.Openat(dir, name, syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		return -1, fmt.Errorf("open credential lock: %w", err)
	}
	if err = checkFile(fd); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	for {
		if err = ctx.Err(); err != nil {
			syscall.Close(fd)
			return -1, err
		}
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN && err != syscall.EINTR {
			syscall.Close(fd)
			return -1, fmt.Errorf("lock credential: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			syscall.Close(fd)
			return -1, ctx.Err()
		case <-timer.C:
		}
	}
	if err = checkFile(fd); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	// Reject a replaced lock inode, even if we successfully locked its predecessor.
	var held, current syscall.Stat_t
	if err = syscall.Fstat(fd, &held); err != nil {
		syscall.Close(fd)
		return -1, errors.New("credential lock stat failed")
	}
	check, err := openLeaf(dir, name)
	if err != nil {
		syscall.Close(fd)
		return -1, errors.New("credential lock changed")
	}
	err = syscall.Fstat(check, &current)
	syscall.Close(check)
	if err != nil || held.Dev != current.Dev || held.Ino != current.Ino {
		syscall.Close(fd)
		return -1, errors.New("credential lock changed")
	}
	return fd, nil
}

func validateCredential(c CodexCredential) error {
	if len(c.AccountID) > 256 {
		return errors.New("invalid credential account ID")
	}
	for _, r := range c.AccountID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return errors.New("invalid credential account ID")
		}
	}
	if len(c.AccessToken) == 0 || len(c.RefreshToken) == 0 || len(c.AccessToken) > maxCodexCredentialBytes/2 || len(c.RefreshToken) > maxCodexCredentialBytes/2 {
		return errors.New("invalid credential token length")
	}
	for _, v := range []string{c.AccessToken, c.RefreshToken} {
		for _, r := range v {
			if r < 0x21 || r == 0x7f {
				return errors.New("invalid credential token character")
			}
		}
	}
	return nil
}

func readCredential(dir int, name string) (CodexCredential, error) {
	var c CodexCredential
	fd, err := openLeaf(dir, name)
	if err != nil {
		return c, err
	}
	f := os.NewFile(uintptr(fd), "credential")
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCodexCredentialBytes+1))
	if err != nil {
		return c, errors.New("read credential failed")
	}
	if len(b) > maxCodexCredentialBytes {
		return c, errors.New("credential too large")
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return CodexCredential{}, errors.New("invalid credential data")
	}
	if err = validateCredential(c); err != nil {
		return CodexCredential{}, err
	}
	return c, nil
}

// Load returns os.ErrNotExist when no credential has been committed yet.
func (s *LocalCodexCredentialStore) Load(ctx context.Context) (CodexCredential, error) {
	if err := ctx.Err(); err != nil {
		return CodexCredential{}, err
	}
	dir, name, err := s.openDir(false)
	if err != nil {
		return CodexCredential{}, err
	}
	defer syscall.Close(dir)
	lock, err := lockLeaf(dir, name+".lock", ctx)
	if err != nil {
		return CodexCredential{}, err
	}
	defer syscall.Close(lock)
	if err = ctx.Err(); err != nil {
		return CodexCredential{}, err
	}
	if err = cleanupTemps(dir, name); err != nil {
		return CodexCredential{}, err
	}
	return readCredential(dir, name)
}

// SynchronizeProjection initializes/reconciles the ordinary-Pi consumer under
// the same lock as rotation and logout. A missing file projects signed-out.
func (s *LocalCodexCredentialStore) SynchronizeProjection(ctx context.Context) error {
	return s.ReadForProjection(ctx, func(*CodexCredential) error { return nil })
}

// ReadForProjection reconciles the configured consumer before the callback,
// holding the existing cross-process credential lock through status/catalog
// commit. Peer writes and absence cannot leave a stale ordinary credential.
func (s *LocalCodexCredentialStore) ReadForProjection(ctx context.Context, consume func(*CodexCredential) error) error {
	if consume == nil {
		return errors.New("missing credential projection")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, name, err := s.openDir(false)
	if err != nil {
		return err
	}
	defer syscall.Close(dir)
	lock, err := lockLeaf(dir, name+".lock", ctx)
	if err != nil {
		return err
	}
	defer syscall.Close(lock)
	if err = cleanupTemps(dir, name); err != nil {
		return err
	}
	credential, err := readCredential(dir, name)
	var current *CodexCredential
	if err == nil {
		current = &credential
	} else if !errors.Is(err, syscall.ENOENT) {
		return err
	}
	if err = s.project(ctx, current); err != nil {
		return err
	}
	return consume(current)
}

// Save atomically replaces a complete record while holding the same stable
// cross-process lock used by Update.
func (s *LocalCodexCredentialStore) Save(ctx context.Context, cred CodexCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCredential(cred); err != nil {
		return err
	}
	dir, name, err := s.openDir(true)
	if err != nil {
		return err
	}
	defer syscall.Close(dir)
	lock, err := lockLeaf(dir, name+".lock", ctx)
	if err != nil {
		return err
	}
	defer syscall.Close(lock)
	if err = cleanupTemps(dir, name); err != nil {
		return err
	}
	if err := replaceCredential(ctx, dir, name, cred); err != nil {
		return err
	}
	return s.project(ctx, &cred)
}

// Delete signs out under the same lock as login and refresh. Sync the parent
// before reporting success so the next request/restart cannot revive the file.
func (s *LocalCodexCredentialStore) Delete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, name, err := s.openDir(false)
	if err != nil {
		return err
	}
	defer syscall.Close(dir)
	lock, err := lockLeaf(dir, name+".lock", ctx)
	if err != nil {
		return err
	}
	defer syscall.Close(lock)
	if err = cleanupTemps(dir, name); err != nil {
		return err
	}
	if err = checkLeafIfExists(dir, name); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = unix.Unlinkat(dir, name, 0); err != nil && !errors.Is(err, syscall.ENOENT) {
		return errors.New("remove credential failed")
	}
	if err = syscall.Fsync(dir); err != nil {
		return errors.New("sync credential directory failed")
	}
	return s.project(ctx, nil)
}

// Update serializes the read, callback and replacement across store instances.
// A missing credential is passed to the callback as a zero value.
func (s *LocalCodexCredentialStore) Update(ctx context.Context, callback func(CodexCredential) (CodexCredential, error)) error {
	if callback == nil {
		return errors.New("nil credential callback")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, name, err := s.openDir(true)
	if err != nil {
		return err
	}
	defer syscall.Close(dir)
	lock, err := lockLeaf(dir, name+".lock", ctx)
	if err != nil {
		return err
	}
	defer syscall.Close(lock)
	if err = cleanupTemps(dir, name); err != nil {
		return err
	}
	current, err := readCredential(dir, name)
	if err != nil && !errors.Is(err, syscall.ENOENT) {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	next, err := callback(current)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = validateCredential(next); err != nil {
		return err
	}
	if err := replaceCredential(ctx, dir, name, next); err != nil {
		return err
	}
	return s.project(ctx, &next)
}

func replaceCredential(ctx context.Context, dir int, name string, c CodexCredential) error {
	b, err := json.Marshal(c)
	if err != nil || len(b) > maxCodexCredentialBytes {
		return errors.New("credential serialization failed or too large")
	}
	if err = checkLeafIfExists(dir, name); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return errors.New("credential temporary name failed")
	}
	temp := name + ".tmp-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(dir, temp, syscall.O_WRONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_EXCL|syscall.O_CREAT, 0o600)
	if err != nil {
		return errors.New("create credential temporary file failed")
	}
	defer func() {
		if unix.Unlinkat(dir, temp, 0) == nil {
			_ = syscall.Fsync(dir) // Best effort on a failed write; never report success.
		}
	}()
	f := os.NewFile(uintptr(fd), "credential temporary file")
	defer f.Close()
	if err = checkFile(fd); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	n, err := f.Write(b)
	if err != nil || n != len(b) {
		return errors.New("write credential failed")
	}
	if err = f.Sync(); err != nil {
		return errors.New("sync credential failed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = checkLeafIfExists(dir, name); err != nil {
		return err
	}
	if err = unix.Renameat(dir, temp, dir, name); err != nil {
		return errors.New("replace credential failed")
	}
	if err = syscall.Fsync(dir); err != nil {
		return errors.New("sync credential directory failed")
	}
	return nil
}

// Scan in bounded batches, validate all matching leftovers before removing any,
// and never follow or erase an unsafe match. Called only while holding the lock.
func cleanupTemps(dir int, name string) error {
	const maxEntries, maxTemps = 4096, 32
	dup, err := syscall.Dup(dir)
	if err != nil {
		return errors.New("open credential directory scan failed")
	}
	d := os.NewFile(uintptr(dup), "credential directory")
	defer d.Close()
	prefix := name + ".tmp-"
	var temps []string
	count := 0
	for {
		batch, e := d.Readdirnames(64)
		count += len(batch)
		if count > maxEntries {
			return errors.New("credential directory scan limit exceeded")
		}
		for _, entry := range batch {
			if !strings.HasPrefix(entry, prefix) || len(entry) != len(prefix)+32 {
				continue
			}
			suffix := entry[len(prefix):]
			valid := true
			for _, ch := range suffix {
				if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			temps = append(temps, entry)
			if len(temps) > maxTemps {
				return errors.New("credential temporary file limit exceeded")
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return errors.New("list credential temporary files failed")
		}
	}
	for _, entry := range temps {
		fd, e := openLeaf(dir, entry)
		if e != nil {
			return errors.New("unsafe credential temporary file")
		}
		var st syscall.Stat_t
		e = syscall.Fstat(fd, &st)
		syscall.Close(fd)
		if e != nil || st.Size < 0 || st.Size > maxCodexCredentialBytes {
			return errors.New("unsafe credential temporary size")
		}
	}
	if len(temps) == 0 {
		return nil
	}
	for _, entry := range temps {
		if err = unix.Unlinkat(dir, entry, 0); err != nil {
			return errors.New("remove credential temporary file failed")
		}
	}
	if err = syscall.Fsync(dir); err != nil {
		return errors.New("sync credential directory failed")
	}
	return nil
}
