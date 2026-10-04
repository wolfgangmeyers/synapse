package managed

import (
	"bytes"
	"context"
	"encoding/base64"
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
const embeddedImageReadLimit = 10 * 1024 * 1024 // 10 MiB

func imageMimeType(p string) (string, bool) {
	ext := strings.ToLower(path.Ext(p))
	switch ext {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".webp":
		return "image/webp", true
	case ".gif":
		return "image/gif", true
	case ".bmp":
		return "image/bmp", true
	default:
		return "", false
	}
}

// The saved root or worker launch Cwd is a trusted project root.
// Its descriptor is pinned for this session; a sticky 1777 parent is fine.
// This does NOT defend against a same-UID adversary renaming directories or
// changing files inside the root concurrently.
func pinEmbeddedReadRoot(cwd string) (int, error) {
	if !path.IsAbs(cwd) {
		return -1, errors.New("read root must be absolute")
	}
	fd, err := openRootDirectory(cwd)
	if err != nil {
		return -1, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		unix.Close(fd)
		return -1, errors.New("read root must be a directory owned by router user")
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

func readEmbeddedRootFile(ctx context.Context, rootFD int, p string, offsetVal, limitVal any) (agent.AgentToolResult, error) {
	if err := ctx.Err(); err != nil {
		return agent.AgentToolResult{}, err
	}
	if !validEmbeddedReadPath(p) {
		return agent.AgentToolResult{}, errors.New("read path must be normalized and relative")
	}
	fd, err := openBeneath(rootFD, p)
	if err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("read denied: %w", err)
	}
	f := os.NewFile(uintptr(fd), "embedded-read")
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if !st.Mode().IsRegular() {
		return agent.AgentToolResult{}, errors.New("read requires a regular file of at most 64 KiB")
	}

	mimeType, isImage := imageMimeType(p)
	if isImage {
		if st.Size() > embeddedImageReadLimit {
			return agent.AgentToolResult{}, errors.New("read requires a regular file of at most 10 MiB")
		}
		data, err := io.ReadAll(io.LimitReader(f, embeddedImageReadLimit+1))
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		if int64(len(data)) > embeddedImageReadLimit {
			return agent.AgentToolResult{}, errors.New("read exceeds 10 MiB limit")
		}
		return agent.AgentToolResult{
			Content: ai.ContentList{
				ai.ImageContent{
					Data:     base64.StdEncoding.EncodeToString(data),
					MimeType: mimeType,
				},
			},
		}, nil
	}

	if st.Size() > embeddedReadLimit {
		return agent.AgentToolResult{}, errors.New("read requires a regular file of at most 64 KiB")
	}
	var result bytes.Buffer
	buf := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			return agent.AgentToolResult{}, err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			if result.Len()+n > embeddedReadLimit {
				return agent.AgentToolResult{}, errors.New("read exceeds 64 KiB")
			}
			result.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return agent.AgentToolResult{}, readErr
		}
	}
	if err := ctx.Err(); err != nil {
		return agent.AgentToolResult{}, err
	}
	data := result.Bytes()
	var offset, limit int
	if offsetVal != nil {
		switch v := offsetVal.(type) {
		case int:
			offset = v
		case int64:
			offset = int(v)
		case float64:
			offset = int(v)
		}
	}
	if limitVal != nil {
		switch v := limitVal.(type) {
		case int:
			limit = v
		case int64:
			limit = int(v)
		case float64:
			limit = int(v)
		}
	}
	if offset < 0 {
		return agent.AgentToolResult{}, errors.New("offset must be non-negative")
	}
	if limit < 0 {
		return agent.AgentToolResult{}, errors.New("limit must be non-negative")
	}
	lines := strings.Split(string(data), "\n")
	if offset > 0 {
		start := offset - 1
		if start >= len(lines) {
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: ""}}}, nil
		}
		lines = lines[start:]
	}
	if limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}
	return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: strings.Join(lines, "\n")}}}, nil
}

func embeddedReadSchema() *ai.Schema {
	closed := false
	schema := ai.Object(
		ai.Prop("path", ai.String()),
		ai.Opt("offset", ai.Integer()),
		ai.Opt("limit", ai.Integer()),
	)
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
		offsetVal := args["offset"]
		limitVal := args["limit"]
		return readEmbeddedRootFile(ctx, rootFD, p, offsetVal, limitVal)
	}}
}
