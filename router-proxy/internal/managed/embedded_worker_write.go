package managed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"golang.org/x/sys/unix"
)

const embeddedWriteLimit = 64 * 1024

// Test-only seams. Nil in normal execution.
var embeddedBeforeWriteParentOpenForTest func()
var embeddedAfterWriteRenameForTest func()

// A durable unknown intent is not proof that this process admitted Execute.
// Only the checked SDK callback can issue this one-shot in-memory admission.
type embeddedWriteAdmission struct {
	binding store.EmbeddedBinding
	id      string
}

func (m *Manager) admitEmbeddedWrite(binding store.EmbeddedBinding, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.embeddedFreshWrites == nil {
		m.embeddedFreshWrites = make(map[embeddedWriteAdmission]struct{})
	}
	m.embeddedFreshWrites[embeddedWriteAdmission{binding, id}] = struct{}{}
}

func (m *Manager) consumeEmbeddedWrite(binding store.EmbeddedBinding, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := embeddedWriteAdmission{binding, id}
	_, fresh := m.embeddedFreshWrites[key]
	delete(m.embeddedFreshWrites, key)
	return fresh
}

func embeddedWriteSchema() *ai.Schema {
	closed := false
	schema := ai.Object(ai.Prop("path", ai.String()), ai.Prop("content", ai.String()))
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedWorkerWriteTool(owner embeddedOwner, rootFD int, consume func(string) bool) agent.AgentTool {
	return agent.AgentTool{Name: "write", Description: "Create a bounded new file inside this session's saved private project root; existing files are never replaced.", Parameters: embeddedWriteSchema(), Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return agent.AgentToolResult{}, err
		}
		role, err := m.store.EmbeddedSessionRole(ctx, owner.binding)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if role != "root" && role != "worker" {
			return agent.AgentToolResult{}, errors.New("write requires embedded root or worker")
		}
		p, pathOK := args["path"].(string)
		content, contentOK := args["content"].(string)
		if !pathOK || len(p) > 4096 || !validEmbeddedReadPath(p) || !contentOK || len(content) > embeddedWriteLimit || !embeddedWriteSchema().Check(args) {
			return agent.AgentToolResult{}, errors.New("invalid write arguments")
		}
		effect, err := m.store.RecoverEmbeddedEffect(ctx, owner.binding, id)
		if err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("write intent unknown: %w", err)
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
		if err != nil || original.ID != id || original.Name != "write" || sha256.Sum256(before) != sha256.Sum256(now) || effect.State != "unknown" {
			return agent.AgentToolResult{}, errors.New("write call conflicts with durable intent")
		}
		if !consume(id) || !m.consumeEmbeddedWrite(owner.binding, id) {
			return agent.AgentToolResult{}, errors.New("write outcome unknown: no fresh SDK admission")
		}
		if err := createEmbeddedWorkerFile(ctx, rootFD, p, []byte(content)); err != nil {
			return agent.AgentToolResult{}, err
		}
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "Successfully wrote " + p}}}, nil
	}}
}

// All path operations stay relative to pinned descriptors. This protects against
// symlink traversal, not an adversarial same-UID process mutating the private tree.
func createEmbeddedWorkerFile(ctx context.Context, rootFD int, p string, content []byte) error {
	if len(p) > 4096 || !validEmbeddedReadPath(p) || len(content) > embeddedWriteLimit {
		return errors.New("write path or content denied")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parts := strings.Split(p, "/")
	parent, err := unix.Openat(rootFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parent) }()
	for _, component := range parts[:len(parts)-1] {
		if err := ctx.Err(); err != nil {
			return err
		}
		if embeddedBeforeWriteParentOpenForTest != nil {
			embeddedBeforeWriteParentOpenForTest()
		}
		fd, openErr := unix.Openat(parent, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if err := unix.Mkdirat(parent, component, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
				return fmt.Errorf("write parent denied: %w", err)
			}
			fd, openErr = unix.Openat(parent, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if openErr == nil {
				// Make a newly created directory durable before writing beneath it.
				if err := unix.Fsync(parent); err != nil {
					_ = unix.Close(fd)
					return err
				}
			}
		}
		if openErr != nil {
			return fmt.Errorf("write parent denied: %w", openErr)
		}
		_ = unix.Close(parent)
		parent = fd
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".mecha-write-" + hex.EncodeToString(nonce[:])
	fd, err := unix.Openat(parent, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("write temporary create denied: %w", err)
	}
	defer func() { _ = unix.Unlinkat(parent, tmp, 0) }()
	f := os.NewFile(uintptr(fd), "embedded-write")
	for len(content) > 0 {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return err
		}
		n, err := f.Write(content)
		if err != nil {
			_ = f.Close()
			return err
		}
		content = content[n:]
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := renameatNoReplace(parent, tmp, parent, parts[len(parts)-1]); err != nil {
		return fmt.Errorf("write create-only conflict or denial: %w", err)
	}
	if embeddedAfterWriteRenameForTest != nil {
		embeddedAfterWriteRenameForTest()
	}
	// A failure here leaves a visible unknown outcome. Never retry this intent.
	if err := unix.Fsync(parent); err != nil {
		return fmt.Errorf("write outcome unknown after rename: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("write outcome unknown after rename: %w", err)
	}
	return nil
}
