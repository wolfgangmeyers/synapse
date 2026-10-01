package managed

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"golang.org/x/sys/unix"
)

var embeddedEditLocks sync.Map // path keyed by pinned root identity; in-process only

func embeddedEditSchema() *ai.Schema {
	closed := false
	change := ai.Object(ai.Prop("oldText", ai.String()), ai.Prop("newText", ai.String()))
	change.AdditionalAllowed = &closed
	schema := ai.Object(ai.Prop("path", ai.String()), ai.Prop("edits", ai.ArrayOf(change)))
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedProjectEditTool(owner embeddedOwner, rootFD int, consume func(string) bool) agent.AgentTool {
	return agent.AgentTool{Name: "edit", Description: "Replace one unique nonempty literal in a bounded existing regular file inside this session's saved private project root.", Parameters: embeddedEditSchema(), Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		if !consume(id) {
			return agent.AgentToolResult{}, errors.New("edit outcome unknown: no fresh SDK admission")
		}
		checkOwner := func() error {
			if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
				return err
			}
			role, err := m.store.EmbeddedSessionRole(ctx, owner.binding)
			if err != nil {
				return err
			}
			if role != "root" && role != "worker" {
				return errors.New("edit requires embedded root or worker")
			}
			return nil
		}
		if err := checkOwner(); err != nil {
			return agent.AgentToolResult{}, err
		}
		p, pathOK := args["path"].(string)
		edits, editsOK := args["edits"].([]any)
		if !pathOK || len(p) > 4096 || !validEmbeddedReadPath(p) || !editsOK || len(edits) != 1 || !embeddedEditSchema().Check(args) {
			return agent.AgentToolResult{}, errors.New("invalid edit arguments")
		}
		change, ok := edits[0].(map[string]any)
		if !ok {
			return agent.AgentToolResult{}, errors.New("invalid edit replacement")
		}
		old, oldOK := change["oldText"].(string)
		replacement, newOK := change["newText"].(string)
		if !oldOK || len(old) == 0 || len(old) > embeddedReadLimit || !newOK || len(replacement) > embeddedReadLimit {
			return agent.AgentToolResult{}, errors.New("invalid edit replacement")
		}
		effect, err := m.store.RecoverEmbeddedEffect(ctx, owner.binding, id)
		if err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("edit intent unknown: %w", err)
		}
		var original struct {
			ID        string         `json:"id"`
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(effect.Intent, &original); err != nil {
			return agent.AgentToolResult{}, err
		}
		before, err := json.Marshal(original.Arguments)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		now, err := json.Marshal(args)
		if err != nil || original.ID != id || original.Name != "edit" || !bytes.Equal(before, now) || effect.State != "unknown" {
			return agent.AgentToolResult{}, errors.New("edit call conflicts with durable intent")
		}
		if err := editEmbeddedWorkerFile(ctx, rootFD, p, old, replacement, checkOwner); err != nil {
			return agent.AgentToolResult{}, err
		}
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "Successfully edited " + p}}}, nil
	}}
}

func editEmbeddedWorkerFile(ctx context.Context, rootFD int, p, old, replacement string, checkOwner func() error) error {
	if len(p) > 4096 || !validEmbeddedReadPath(p) || old == "" || len(old) > embeddedReadLimit || len(replacement) > embeddedReadLimit {
		return errors.New("edit path or replacement denied")
	}
	var root unix.Stat_t
	if err := unix.Fstat(rootFD, &root); err != nil {
		return err
	}
	key := fmt.Sprintf("%d:%d:%s", root.Dev, root.Ino, p)
	lockAny, _ := embeddedEditLocks.LoadOrStore(key, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	parts := strings.Split(p, "/")
	parent, err := unix.Openat(rootFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(parent, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return fmt.Errorf("edit parent denied: %w", err)
		}
		unix.Close(parent)
		parent = next
	}
	name := parts[len(parts)-1]
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("edit target denied: %w", err)
	}
	file := os.NewFile(uintptr(fd), "embedded-edit-target")
	defer file.Close()
	var initial unix.Stat_t
	if err := unix.Fstat(fd, &initial); err != nil {
		return err
	}
	if initial.Mode&unix.S_IFMT != unix.S_IFREG || initial.Size > embeddedReadLimit {
		return errors.New("edit requires bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, embeddedReadLimit+1))
	if err != nil {
		return err
	}
	if len(data) > embeddedReadLimit {
		return errors.New("edit original exceeds limit")
	}
	if bytes.Count(data, []byte(old)) != 1 {
		return errors.New("edit oldText must occur exactly once")
	}
	result := bytes.Replace(data, []byte(old), []byte(replacement), 1)
	if len(result) > embeddedReadLimit {
		return errors.New("edit result exceeds limit")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".mecha-edit-" + hex.EncodeToString(nonce[:])
	tempFD, err := unix.Openat(parent, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(parent, tmp, 0)
	temp := os.NewFile(uintptr(tempFD), "embedded-edit-temp")
	if err := unix.Fchmod(tempFD, initial.Mode&0777); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(result); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if checkOwner != nil {
		if err := checkOwner(); err != nil {
			return err
		}
	}
	var current unix.Stat_t
	if err := unix.Fstatat(parent, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("edit target changed: %w", err)
	}
	if current.Mode&unix.S_IFMT != unix.S_IFREG || current.Dev != initial.Dev || current.Ino != initial.Ino || current.Size != initial.Size || current.Mtim != initial.Mtim || current.Ctim != initial.Ctim {
		return errors.New("edit target changed before replace")
	}
	if err := unix.Renameat(parent, tmp, parent, name); err != nil {
		return err
	}
	// Once renamed, every failure is ambiguous and must not be replayed.
	if err := unix.Fsync(parent); err != nil {
		return fmt.Errorf("edit outcome unknown after rename: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("edit outcome unknown after rename: %w", err)
	}
	return nil
}
