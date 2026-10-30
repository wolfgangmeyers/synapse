package managed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestKBTools_Lifecycle(t *testing.T) {
	// Create temporary directory for KB
	tempKB := t.TempDir()
	t.Setenv("MECHA_KB_DIR", tempKB)

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	mgr := &Manager{
		store: db,
	}

	owner := embeddedOwner{
		binding: store.EmbeddedBinding{
			AgentID:    "test-worker",
			SessionID:  "test-session",
			Epoch:      1,
			Generation: "1",
		},
		identity: "test-identity",
	}

	writeTool := mgr.embeddedKBWriteTool(owner)
	readTool := mgr.embeddedKBReadTool(owner)
	listTool := mgr.embeddedKBListTool(owner)
	searchTool := mgr.embeddedKBSearchTool(owner)

	ctx := context.Background()

	// 1. Write a topic
	topic1Content := `## Summary
JWT authentication explanation and token usage.

## Content
JWT tokens are signed JSON objects used for authentication.`

	resWrite, err := writeTool.Execute(ctx, "call-w1", map[string]any{
		"topic":   "JWT Authentication",
		"content": topic1Content,
		"domain":  "Auth",
	}, nil)
	require.NoError(t, err)
	require.Len(t, resWrite.Content, 1)
	require.Contains(t, resWrite.Content[0].(ai.TextContent).Text, "Successfully saved knowledge base topic")

	// Verify file was written
	file1Path := filepath.Join(tempKB, "jwt-authentication.md")
	data, err := os.ReadFile(file1Path)
	require.NoError(t, err)
	require.Contains(t, string(data), "# JWT Authentication")
	require.Contains(t, string(data), "JWT authentication explanation")

	// Verify INDEX.md was created and contains entry
	indexPath := filepath.Join(tempKB, "INDEX.md")
	indexData, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	require.Contains(t, string(indexData), "## Auth")
	require.Contains(t, string(indexData), "[JWT Authentication](jwt-authentication.md)")

	// 2. Write a second topic
	topic2Content := `## Summary
Deployment checklist and release process steps.

## Content
Ensure all automated tests pass before release.`

	resWrite2, err := writeTool.Execute(ctx, "call-w2", map[string]any{
		"topic":   "Deployment Checklist",
		"content": topic2Content,
		"domain":  "Infrastructure",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resWrite2.Content[0].(ai.TextContent).Text, "Successfully saved")

	// 3. Read topic by slug
	resRead, err := readTool.Execute(ctx, "call-r1", map[string]any{
		"topic": "jwt-authentication",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resRead.Content[0].(ai.TextContent).Text, "JWT authentication explanation")

	// 4. Read topic by path
	resRead2, err := readTool.Execute(ctx, "call-r2", map[string]any{
		"path": "deployment-checklist.md",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resRead2.Content[0].(ai.TextContent).Text, "Deployment checklist")

	// 5. Test path traversal rejection
	resTraversal, err := readTool.Execute(ctx, "call-r3", map[string]any{
		"path": "../../etc/passwd",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resTraversal.Content[0].(ai.TextContent).Text, "traversal denied")

	// 6. List all topics
	resList, err := listTool.Execute(ctx, "call-l1", map[string]any{}, nil)
	require.NoError(t, err)
	listText := resList.Content[0].(ai.TextContent).Text
	require.Contains(t, listText, "JWT Authentication")
	require.Contains(t, listText, "Deployment Checklist")

	// 7. List filtered by domain
	resListAuth, err := listTool.Execute(ctx, "call-l2", map[string]any{
		"domain": "Auth",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resListAuth.Content[0].(ai.TextContent).Text, "JWT Authentication")
	require.NotContains(t, resListAuth.Content[0].(ai.TextContent).Text, "Deployment Checklist")

	// 8. Search (fallback to keyword match when System 1 is unconfigured)
	resSearch, err := searchTool.Execute(ctx, "call-s1", map[string]any{
		"query":     "JWT authentication tokens",
		"threshold": 0.2,
		"limit":     5,
	}, nil)
	require.NoError(t, err)
	searchText := resSearch.Content[0].(ai.TextContent).Text
	require.Contains(t, searchText, "jwt-authentication.md")
	require.Contains(t, searchText, "keyword matching fallback")
}

func TestKBTools_SystemOneSearch(t *testing.T) {
	tempKB := t.TempDir()
	t.Setenv("MECHA_KB_DIR", tempKB)

	// Write two test files
	_ = os.WriteFile(filepath.Join(tempKB, "doc1.md"), []byte("# Golang Guide\n\n## Summary\nConcurrency patterns in Go.\n\n## Content\nGoroutines and channels."), 0o644)
	_ = os.WriteFile(filepath.Join(tempKB, "doc2.md"), []byte("# Cooking Recipe\n\n## Summary\nHow to bake bread.\n\n## Content\nFlour, water, yeast."), 0o644)

	// Mock System 1 HTTP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return noul: 0.95 for q_0 (doc1), 0.05 for q_1 (doc2)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"model": "clef",
			"answers": {
				"q_0": {"type": "noul", "noul": 0.95},
				"q_1": {"type": "noul", "noul": 0.05}
			}
		}`))
	}))
	defer mockServer.Close()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	// Parse mock server host and port
	hostPort := strings.TrimPrefix(mockServer.URL, "http://")
	parts := strings.Split(hostPort, ":")
	require.Len(t, parts, 2)
	port := 0
	for _, ch := range parts[1] {
		port = port*10 + int(ch-'0')
	}

	err = db.SetSystemOneConfig(context.Background(), store.SystemOneConfig{
		Host:    parts[0],
		Port:    port,
		Enabled: true,
	})
	require.NoError(t, err)

	mgr := &Manager{
		store: db,
	}

	owner := embeddedOwner{
		binding: store.EmbeddedBinding{
			AgentID:    "test-worker",
			SessionID:  "test-session",
			Epoch:      1,
			Generation: "1",
		},
		identity: "test-identity",
	}

	searchTool := mgr.embeddedKBSearchTool(owner)

	// Search for golang concurrency with threshold 0.5
	resSearch, err := searchTool.Execute(context.Background(), "call-s1", map[string]any{
		"query":     "Golang goroutines and channels",
		"threshold": 0.5,
	}, nil)
	require.NoError(t, err)
	resultText := resSearch.Content[0].(ai.TextContent).Text
	require.Contains(t, resultText, "System 1 calibrated relevance")
	require.Contains(t, resultText, "doc1.md")
	require.NotContains(t, resultText, "doc2.md")
}

func TestKBTools_SystemOneSearch_FallbackOnError(t *testing.T) {
	tempKB := t.TempDir()
	t.Setenv("MECHA_KB_DIR", tempKB)

	// Write two test files
	_ = os.WriteFile(filepath.Join(tempKB, "doc1.md"), []byte("# Golang Guide\n\n## Summary\nConcurrency patterns in Go.\n\n## Content\nGoroutines and channels."), 0o644)
	_ = os.WriteFile(filepath.Join(tempKB, "doc2.md"), []byte("# Cooking Recipe\n\n## Summary\nHow to bake bread.\n\n## Content\nFlour, water, yeast."), 0o644)

	// Mock System 1 HTTP server that returns 502 Bad Gateway
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error": "ollama connection failed"}`))
	}))
	defer mockServer.Close()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	hostPort := strings.TrimPrefix(mockServer.URL, "http://")
	parts := strings.Split(hostPort, ":")
	require.Len(t, parts, 2)
	port := 0
	for _, ch := range parts[1] {
		port = port*10 + int(ch-'0')
	}

	err = db.SetSystemOneConfig(context.Background(), store.SystemOneConfig{
		Host:    parts[0],
		Port:    port,
		Enabled: true,
	})
	require.NoError(t, err)

	mgr := &Manager{
		store: db,
	}

	owner := embeddedOwner{
		binding: store.EmbeddedBinding{
			AgentID:    "test-worker",
			SessionID:  "test-session",
			Epoch:      1,
			Generation: "1",
		},
		identity: "test-identity",
	}

	searchTool := mgr.embeddedKBSearchTool(owner)

	// Search should gracefully fall back to keyword search and include error details
	resSearch, err := searchTool.Execute(context.Background(), "call-fallback", map[string]any{
		"query":     "Golang goroutines and channels",
		"threshold": 0.2,
	}, nil)
	require.NoError(t, err)
	resultText := resSearch.Content[0].(ai.TextContent).Text
	require.Contains(t, resultText, "keyword matching fallback; system 1 error:")
	require.Contains(t, resultText, "doc1.md")

	details, ok := resSearch.Details.(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, details["fallback"])
	require.NotEmpty(t, details["fallback_error"])

	// Test zero results case includes fallback note and error
	resEmpty, err := searchTool.Execute(context.Background(), "call-empty", map[string]any{
		"query":     "astronomy telescope stars galaxy",
		"threshold": 0.8,
	}, nil)
	require.NoError(t, err)
	emptyText := resEmpty.Content[0].(ai.TextContent).Text
	require.Contains(t, emptyText, "using keyword matching fallback; system 1 error:")
}

func TestKBDeleteTool(t *testing.T) {
	tempKB := t.TempDir()
	t.Setenv("MECHA_KB_DIR", tempKB)

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	mgr := &Manager{
		store: db,
	}

	owner := embeddedOwner{
		binding: store.EmbeddedBinding{
			AgentID:    "test-worker",
			SessionID:  "test-session",
			Epoch:      1,
			Generation: "1",
		},
		identity: "test-identity",
	}

	writeTool := mgr.embeddedKBWriteTool(owner)
	deleteTool := mgr.embeddedKBDeleteTool(owner)
	readTool := mgr.embeddedKBReadTool(owner)

	ctx := context.Background()

	// 1. Create a topic
	topicContent := `## Summary
Delete me topic summary.

## Content
This topic will be deleted.`

	resWrite, err := writeTool.Execute(ctx, "call-w1", map[string]any{
		"topic":   "To Be Deleted",
		"content": topicContent,
		"domain":  "Testing",
	}, nil)
	require.NoError(t, err)
	require.Len(t, resWrite.Content, 1)

	fileTarget := filepath.Join(tempKB, "to-be-deleted.md")
	_, err = os.Stat(fileTarget)
	require.NoError(t, err)

	indexPath := filepath.Join(tempKB, "INDEX.md")
	indexBytes, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	require.Contains(t, string(indexBytes), "to-be-deleted.md")
	require.Contains(t, string(indexBytes), "To Be Deleted")

	// 2. Delete the topic by topic name
	resDelete, err := deleteTool.Execute(ctx, "call-d1", map[string]any{
		"topic": "To Be Deleted",
	}, nil)
	require.NoError(t, err)
	require.Len(t, resDelete.Content, 1)
	deleteText := resDelete.Content[0].(ai.TextContent).Text
	require.Contains(t, deleteText, "Successfully deleted knowledge base document")
	require.Contains(t, deleteText, "to-be-deleted.md")

	// Verify file is gone
	_, err = os.Stat(fileTarget)
	require.True(t, os.IsNotExist(err))

	// Verify INDEX.md no longer has the entry
	indexBytes, err = os.ReadFile(indexPath)
	require.NoError(t, err)
	require.NotContains(t, string(indexBytes), "to-be-deleted.md")
	require.NotContains(t, string(indexBytes), "[To Be Deleted]")

	// Verify read tool returns not found
	resRead, err := readTool.Execute(ctx, "call-r1", map[string]any{
		"topic": "to-be-deleted",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resRead.Content[0].(ai.TextContent).Text, "Knowledge base file not found")

	// 3. Test deleting again returns not found
	resDelete2, err := deleteTool.Execute(ctx, "call-d2", map[string]any{
		"topic": "To Be Deleted",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resDelete2.Content[0].(ai.TextContent).Text, "Knowledge base file not found")

	// 4. Test deleting non-existent file directly by path returns error
	resDeleteNonExistent, err := deleteTool.Execute(ctx, "call-d-missing", map[string]any{
		"path": "non-existent-file.md",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resDeleteNonExistent.Content[0].(ai.TextContent).Text, "Knowledge base file not found")

	// 5. Test path traversal rejection
	resDeleteTraversal, err := deleteTool.Execute(ctx, "call-d-trav", map[string]any{
		"path": "../../etc/passwd",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resDeleteTraversal.Content[0].(ai.TextContent).Text, "traversal denied")

	// 6. Test missing parameter error
	resDeleteEmpty, err := deleteTool.Execute(ctx, "call-d-empty", map[string]any{}, nil)
	require.NoError(t, err)
	require.Contains(t, resDeleteEmpty.Content[0].(ai.TextContent).Text, "topic or path parameter must be specified")

	// 7. Deletion via path argument and verify INDEX.md cleans bullet list link while preserving surrounding text/headings
	sampleDocPath := filepath.Join(tempKB, "sample-doc.md")
	err = os.WriteFile(sampleDocPath, []byte("# Sample Doc\n\nSample content"), 0o644)
	require.NoError(t, err)

	keepDocPath := filepath.Join(tempKB, "keep-me.md")
	err = os.WriteFile(keepDocPath, []byte("# Keep Me\n\nKeep content"), 0o644)
	require.NoError(t, err)

	customIndex := `# Knowledge Base Index

## Notes

Important notes regarding documentation structure.

## Testing Domain

- [Sample Doc](sample-doc.md)
- [Keep Me](keep-me.md)

Footer notes and guidelines.`
	err = os.WriteFile(indexPath, []byte(customIndex), 0o644)
	require.NoError(t, err)

	resDeletePath, err := deleteTool.Execute(ctx, "call-d-path", map[string]any{
		"path": "sample-doc.md",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resDeletePath.Content[0].(ai.TextContent).Text, "Successfully deleted knowledge base document")
	require.Contains(t, resDeletePath.Content[0].(ai.TextContent).Text, "sample-doc.md")

	// Verify sample-doc.md was deleted
	_, err = os.Stat(sampleDocPath)
	require.True(t, os.IsNotExist(err))

	// Verify keep-me.md still exists
	_, err = os.Stat(keepDocPath)
	require.NoError(t, err)

	// Verify INDEX.md: sample-doc is removed, but headings and surrounding text are preserved
	updatedIndexBytes, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	updatedIndex := string(updatedIndexBytes)

	require.NotContains(t, updatedIndex, "[Sample Doc](sample-doc.md)")
	require.Contains(t, updatedIndex, "- [Keep Me](keep-me.md)")
	require.Contains(t, updatedIndex, "## Notes")
	require.Contains(t, updatedIndex, "Important notes regarding documentation structure.")
	require.Contains(t, updatedIndex, "## Testing Domain")
	require.Contains(t, updatedIndex, "Footer notes and guidelines.")
}


