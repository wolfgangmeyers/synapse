package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"golang.org/x/sys/unix"
)

const embeddedReadLimit = 64 * 1024

// The saved root or worker launch Cwd is a trusted, private project root.
// Its descriptor is pinned for this session; a sticky 1777 parent is fine.
// This does NOT defend against a same-UID adversary renaming directories or
// changing files inside the private root concurrently.
func pinEmbeddedReadRoot(cwd string) (int, error) {
	if !path.IsAbs(cwd) {
		return -1, errors.New("read root must be absolute")
	}
	fd, err := unix.Open(cwd, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0077 != 0 {
		unix.Close(fd)
		return -1, errors.New("read root must be private and owned by router user")
	}
	return fd, nil
}

func validEmbeddedReadPath(p string) bool {
	if p == "" || p == "." || strings.HasPrefix(p, "~") || strings.Contains(p, ":") || path.IsAbs(p) || strings.Contains(p, "\\") || path.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func readEmbeddedRootFile(ctx context.Context, rootFD int, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validEmbeddedReadPath(p) {
		return "", errors.New("read path must be normalized and relative")
	}
	fd, err := unix.Openat2(rootFD, p, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return "", fmt.Errorf("read denied: %w", err)
	}
	f := os.NewFile(uintptr(fd), "embedded-read")
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Size() > embeddedReadLimit {
		return "", errors.New("read requires a regular file of at most 64 KiB")
	}
	var result bytes.Buffer
	buf := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			if result.Len()+n > embeddedReadLimit {
				return "", errors.New("read exceeds 64 KiB")
			}
			result.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return result.String(), nil
}

func embeddedReadSchema() *ai.Schema {
	closed := false
	schema := ai.Object(ai.Prop("path", ai.String()))
	schema.AdditionalAllowed = &closed
	return schema
}

func (m *Manager) embeddedRootReadTool(owner embeddedOwner, rootFD int) agent.AgentTool {
	return agent.AgentTool{Name: "read", Description: "Read a bounded regular file inside the saved private project root.", Parameters: embeddedReadSchema(), Execute: func(ctx context.Context, id string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
			return agent.AgentToolResult{}, err
		}
		role, err := m.store.EmbeddedSessionRole(ctx, owner.binding)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if role != "root" && role != "worker" {
			return agent.AgentToolResult{}, errors.New("read requires embedded root or worker")
		}
		p, ok := args["path"].(string)
		if !ok || !embeddedReadSchema().Check(args) {
			return agent.AgentToolResult{}, errors.New("invalid read arguments")
		}
		effect, err := m.store.RecoverEmbeddedEffect(ctx, owner.binding, id)
		if err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("read intent unknown: %w", err)
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
		if err != nil || original.ID != id || original.Name != "read" || !bytes.Equal(before, now) {
			return agent.AgentToolResult{}, errors.New("read call conflicts with durable intent")
		}
		text, err := readEmbeddedRootFile(ctx, rootFD, p)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: text}}}, nil
	}}
}
