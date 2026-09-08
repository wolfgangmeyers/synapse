package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// sessionArtifactOwner is the sole managed filesystem boundary. Store rows hold
// only a root-relative identity; every discovery and unlink reopens SessionRoot
// and proves containment again before it touches a file.
type sessionArtifactOwner struct {
	store *store.Store
	root  string
	ids   deterministic.IDSource
	now   func() time.Time
}

func newSessionArtifactOwner(s *store.Store, root string, ids deterministic.IDSource, now func() time.Time) *sessionArtifactOwner {
	return &sessionArtifactOwner{store: s, root: root, ids: ids, now: now}
}

func (o *sessionArtifactOwner) timestamp() string { return formatManagedTime(o.now()) }

func sessionFileFromState(state map[string]any) (string, error) {
	for _, key := range []string{"sessionFile", "session_file"} {
		if value, ok := state[key].(string); ok && value != "" {
			return value, nil
		}
	}
	return "", errors.New("Pi get_state response has no session file")
}

// ensureUnusedSessionID rejects any existing file or directory whose basename
// is the candidate UUID. Pi keeps session identities in its controlled session
// root, so this gives CreateNew a bounded filesystem precondition without
// inspecting session contents or accepting a caller-provided path.
func (o *sessionArtifactOwner) ensureUnusedSessionID(sessionID string) error {
	root, err := o.rootPath()
	if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("managed session root has a symlink")
		}
		if strings.Contains(entry.Name(), sessionID) {
			return errors.New("managed session ID already exists under session root")
		}
		return nil
	})
}

func (o *sessionArtifactOwner) rootPath() (string, error) {
	if o == nil || o.root == "" {
		return "", errors.New("managed session root is required")
	}
	root, err := filepath.Abs(o.root)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("managed session root is unsafe")
	}
	return root, nil
}

// rootRelativeSessionPath accepts the reported session file only if it names a
// file beneath SessionRoot. Absolute responses are normalized to a relative
// ledger value after containment has been proven; paths outside the root and
// all symlink components are rejected.
func (o *sessionArtifactOwner) rootRelativeSessionPath(reported string) (root, relative, absolute string, info fs.FileInfo, absent bool, err error) {
	root, err = o.rootPath()
	if err != nil {
		return "", "", "", nil, false, err
	}
	if reported == "" {
		return "", "", "", nil, false, errors.New("Pi session file is empty")
	}
	candidate := filepath.Clean(reported)
	if filepath.IsAbs(candidate) {
		relative, err = filepath.Rel(root, candidate)
	} else {
		relative = candidate
	}
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", "", "", nil, false, errors.New("Pi session file escapes managed session root")
	}
	relative = filepath.Clean(relative)
	absolute = filepath.Join(root, relative)
	// Lstat each existing component. EvalSymlinks is intentionally not used:
	// resolving a symlink would normalize a path this owner must reject.
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", "", nil, false, errors.New("Pi session file is invalid")
		}
		current = filepath.Join(current, part)
		candidateInfo, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if index != len(parts)-1 {
				return "", "", "", nil, false, errors.New("Pi session file has a missing parent")
			}
			return root, filepath.ToSlash(relative), absolute, nil, true, nil
		}
		if statErr != nil {
			return "", "", "", nil, false, statErr
		}
		if candidateInfo.Mode()&os.ModeSymlink != 0 {
			return "", "", "", nil, false, errors.New("Pi session file has a symlink component")
		}
		if index != len(parts)-1 && !candidateInfo.IsDir() {
			return "", "", "", nil, false, errors.New("Pi session file parent is not a directory")
		}
		if index == len(parts)-1 {
			if !candidateInfo.Mode().IsRegular() {
				return "", "", "", nil, false, errors.New("Pi session file is not regular")
			}
			return root, filepath.ToSlash(relative), absolute, candidateInfo, false, nil
		}
	}
	return "", "", "", nil, false, errors.New("Pi session file is invalid")
}

func deviceInode(info fs.FileInfo) (string, string, error) {
	if info == nil || !info.Mode().IsRegular() {
		return "", "", errors.New("session artifact is not regular")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", errors.New("session artifact has no device identity")
	}
	return fmt.Sprintf("%d", stat.Dev), fmt.Sprintf("%d", stat.Ino), nil
}

func artifactPathHash(relative string) string {
	sum := sha256.Sum256([]byte(relative))
	return fmt.Sprintf("%x", sum[:])
}

// discover accepts session-file evidence only from get_state. It records an
// existing regular file with its device/inode pair, or an exact missing final
// candidate as not_materialized. It never guesses a Pi filename.
func (o *sessionArtifactOwner) discover(ctx context.Context, agentID string, epoch int64, sessionID string, state map[string]any) (*store.ManagedSessionArtifactRecord, error) {
	if !stateSessionMatches(state, sessionID) {
		return nil, errors.New("Pi session artifact identity does not match managed session")
	}
	reported, err := sessionFileFromState(state)
	if err != nil {
		return nil, err
	}
	_, relative, _, info, absent, err := o.rootRelativeSessionPath(reported)
	if err != nil {
		return nil, err
	}
	discovery, device, inode := "not_materialized", "", ""
	if !absent {
		discovery = "materialized"
		device, inode, err = deviceInode(info)
		if err != nil {
			return nil, err
		}
	}
	now := o.timestamp()
	existing, lookupErr := o.store.ManagedSessionArtifactForSession(ctx, agentID, epoch, sessionID)
	if lookupErr == nil {
		return o.store.RefreshManagedSessionArtifact(ctx, store.ManagedSessionArtifact{
			ArtifactID: existing.ArtifactID, AgentID: agentID, SessionEpoch: epoch, PiSessionID: sessionID,
			RootRelativePath: relative, PathSHA256: artifactPathHash(relative), DiscoveryState: discovery,
			FileDevice: device, FileInode: inode, CleanupState: "retained", DiscoveredAt: now, CleanupUpdatedAt: now,
		})
	}
	if !errors.Is(lookupErr, sql.ErrNoRows) {
		return nil, lookupErr
	}
	artifactID, err := o.ids.Next()
	if err != nil {
		return nil, err
	}
	return o.store.RefreshManagedSessionArtifact(ctx, store.ManagedSessionArtifact{
		ArtifactID: artifactID, AgentID: agentID, SessionEpoch: epoch, PiSessionID: sessionID,
		RootRelativePath: relative, PathSHA256: artifactPathHash(relative), DiscoveryState: discovery,
		FileDevice: device, FileInode: inode, CleanupState: "retained", DiscoveredAt: now, CleanupUpdatedAt: now,
	})
}

func (o *sessionArtifactOwner) safeAbsence(record *store.ManagedSessionArtifactRecord) (bool, error) {
	if record == nil {
		return false, errors.New("session artifact is missing")
	}
	_, relative, _, info, absent, err := o.rootRelativeSessionPath(record.RootRelativePath)
	if err != nil || relative != record.RootRelativePath {
		return false, err
	}
	if absent {
		return true, nil
	}
	if record.DiscoveryState != "materialized" {
		return false, errors.New("not-materialized artifact is now unexpectedly present")
	}
	device, inode, err := deviceInode(info)
	if err != nil || device != record.FileDevice || inode != record.FileInode {
		return false, errors.New("session artifact identity changed")
	}
	return false, nil
}

// unlink moves retained -> pending before touching disk. A crash after unlink
// is resolved by the same fresh absence check. Any uncertainty returns the
// artifact to retained and reports failure; deletion is never best effort.
func (o *sessionArtifactOwner) unlink(ctx context.Context, artifactID string) error {
	record, err := o.store.ManagedSessionArtifact(ctx, artifactID)
	if err != nil {
		return err
	}
	if record.CleanupState == "unlinked" {
		return nil
	}
	if record.CleanupState == "retained" {
		if err := o.store.MarkManagedSessionArtifactCleanup(ctx, artifactID, "retained", "pending"); err != nil {
			return err
		}
		record, err = o.store.ManagedSessionArtifact(ctx, artifactID)
		if err != nil {
			return err
		}
	}
	if record.CleanupState != "pending" {
		return errors.New("session artifact cleanup state is invalid")
	}
	absent, err := o.safeAbsence(record)
	if err != nil {
		_ = o.store.MarkManagedSessionArtifactCleanup(context.Background(), artifactID, "pending", "retained")
		return err
	}
	if !absent {
		_, _, absolute, _, _, err := o.rootRelativeSessionPath(record.RootRelativePath)
		if err != nil {
			_ = o.store.MarkManagedSessionArtifactCleanup(context.Background(), artifactID, "pending", "retained")
			return err
		}
		if err := os.Remove(absolute); err != nil {
			_ = o.store.MarkManagedSessionArtifactCleanup(context.Background(), artifactID, "pending", "retained")
			return err
		}
		absent, err = o.safeAbsence(record)
		if err != nil || !absent {
			_ = o.store.MarkManagedSessionArtifactCleanup(context.Background(), artifactID, "pending", "retained")
			if err != nil {
				return err
			}
			return errors.New("session artifact remained after unlink")
		}
	}
	return o.store.MarkManagedSessionArtifactCleanup(ctx, artifactID, "pending", "unlinked")
}
