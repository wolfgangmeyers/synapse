package managed

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/systemone"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestKBTools_LiveSystemOneE2E(t *testing.T) {
	// Verify live router endpoint is running before proceeding
	checkClient := &http.Client{Timeout: 2 * time.Second}
	checkResp, err := checkClient.Get("http://127.0.0.1:8765/v1/systemone")
	if err != nil || checkResp.StatusCode != http.StatusOK {
		t.Skip("Live System 1 router endpoint at http://127.0.0.1:8765/v1/systemone is not available, skipping live E2E test")
	}
	checkResp.Body.Close()

	// 1. Set up temporary KB directory
	tempKB := t.TempDir()
	t.Setenv("MECHA_KB_DIR", tempKB)

	// Populate real sample topic files
	files := map[string]string{
		"postgres-indexing-strategies.md": `# Postgres Indexing Strategies

## Summary
Comprehensive guide to PostgreSQL B-Tree, GIN, and GiST indexes, partial indexing, and query optimization.

## Content
PostgreSQL provides diverse indexing options:
- B-Tree indexes for standard equality and range comparisons.
- GIN (Generalized Inverted Index) for full-text search, arrays, and jsonb queries.
- GiST for geometric data and nearest-neighbor lookups.
- BRIN for large sequential tables.
Use EXPLAIN ANALYZE to verify index usage and build with CREATE INDEX CONCURRENTLY to prevent locking write operations.
`,
		"redis-caching.md": `# Redis Caching

## Summary
In-memory cache setup with Redis, TTL expiration strategies, cache-aside, and eviction policies.

## Content
Redis is used as a fast, high-performance in-memory key-value store for caching session data and API responses.
Key eviction policies such as volatile-lru and allkeys-lru should be tuned according to workload memory constraints.
`,
		"jwt-authentication.md": `# JWT Authentication

## Summary
JSON Web Token validation, signing algorithms, bearer token validation, and refresh token flows in Go.

## Content
JSON Web Tokens (JWT) encapsulate claims signed with HMAC-SHA256 (HS256) or RSA (RS256).
Services validate the bearer token header, verify signature integrity, check expiration and issuer claims, and reject unsigned tokens.
`,
		"docker-builds.md": `# Docker Builds

## Summary
Optimizing Docker image layers, minimizing image size with multi-stage builds, and caching techniques.

## Content
Multi-stage builds allow separating the compilation environment from runtime containers.
Keep images lightweight by copying only necessary artifacts to minimal base images like alpine or scratch, and ordering Dockerfile commands to maximize layer cache hits.
`,
		"system-one-integration.md": `# System 1 Integration

## Summary
How to connect fast decision models via Ollama and the router proxy for structured classification.

## Content
System 1 integration routes requests to Ollama hosting Clef.
It evaluates decisions like Noul probability scores and categorical choices with low latency.
`,
	}

	for filename, content := range files {
		err := os.WriteFile(filepath.Join(tempKB, filename), []byte(content), 0o644)
		require.NoError(t, err)
	}

	// 2. Set up store configured to route System 1 calls to live router at 127.0.0.1:8765
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	err = db.SetSystemOneConfig(context.Background(), store.SystemOneConfig{
		Host:    "127.0.0.1",
		Port:    8765,
		Enabled: true,
	})
	require.NoError(t, err)

	mgr := &Manager{
		store: db,
	}

	owner := embeddedOwner{
		binding: store.EmbeddedBinding{
			AgentID:    "e2e-worker",
			SessionID:  "e2e-session",
			Epoch:      1,
			Generation: "1",
		},
		identity: "e2e-identity",
	}

	// 3. Test raw scoring via scoreWithSystemOne to inspect all returned scores
	ctx := context.Background()
	var entries []kbFileEntry
	for filename, content := range files {
		title, summary := parseTopicHeader(content)
		entries = append(entries, kbFileEntry{
			relPath: filename,
			absPath: filepath.Join(tempKB, filename),
			title:   title,
			summary: summary,
			content: content,
		})
	}

	rawScores, err := mgr.scoreWithSystemOne(ctx, "How do I configure postgres database indexes?", entries)
	require.NoError(t, err)
	require.NotEmpty(t, rawScores)

	t.Logf("=== System 1 Raw Scores for Query: 'How do I configure postgres database indexes?' ===")
	for _, res := range rawScores {
		t.Logf("  Document: %-35s Score: %.5f (Title: %s)", res.Filename, res.Score, res.Title)
	}

	// Verify that postgres-indexing-strategies.md got a high score (> 0.7)
	var postgresScore float64
	for _, res := range rawScores {
		if res.Filename == "postgres-indexing-strategies.md" {
			postgresScore = res.Score
			break
		}
	}
	require.Greater(t, postgresScore, 0.7, "Expected postgres-indexing-strategies.md score > 0.7")

	// Verify unrelated files received low scores (< 0.5)
	for _, res := range rawScores {
		if res.Filename != "postgres-indexing-strategies.md" {
			require.Less(t, res.Score, 0.5, "Expected %s score < 0.5, got %.5f", res.Filename, res.Score)
		}
	}

	// 4. Execute kb_search tool with threshold 0.5
	searchTool := mgr.embeddedKBSearchTool(owner)
	toolRes, err := searchTool.Execute(ctx, "call-e2e-1", map[string]any{
		"query":     "How do I configure postgres database indexes?",
		"threshold": 0.5,
	}, nil)
	require.NoError(t, err)

	resultText := toolRes.Content[0].(ai.TextContent).Text
	t.Logf("=== kb_search Result Text ===\n%s", resultText)

	// Verify tool result content
	require.Contains(t, resultText, "System 1 calibrated relevance")
	require.Contains(t, resultText, "postgres-indexing-strategies.md")
	require.NotContains(t, resultText, "jwt-authentication.md")
	require.NotContains(t, resultText, "redis-caching.md")
	require.NotContains(t, resultText, "docker-builds.md")
	require.NotContains(t, resultText, "system-one-integration.md")

	// Verify tool result details
	detailsMap, ok := toolRes.Details.(map[string]any)
	require.True(t, ok)
	detailsResults, ok := detailsMap["results"].([]KBSearchResult)
	require.True(t, ok)
	require.Len(t, detailsResults, 1, "Only postgres-indexing-strategies.md should pass threshold 0.5")
	require.Equal(t, "postgres-indexing-strategies.md", detailsResults[0].Filename)
	require.Greater(t, detailsResults[0].Score, 0.7)

	fallbackVal, ok := detailsMap["fallback"].(bool)
	require.True(t, ok)
	require.False(t, fallbackVal, "Should use System 1, not keyword fallback")

	// 5. Test unreachable/invalid endpoint fails loudly with descriptive error
	t.Run("UnreachableEndpointFailsLoudly", func(t *testing.T) {
		unreachableDB, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "unreachable.db"), store.Options{})
		require.NoError(t, err)
		defer unreachableDB.Close()

		// Set an unreachable port
		err = unreachableDB.SetSystemOneConfig(context.Background(), store.SystemOneConfig{
			Host:    "127.0.0.1",
			Port:    59998,
			Enabled: true,
		})
		require.NoError(t, err)

		unreachableMgr := &Manager{
			store: unreachableDB,
		}

		_, scoreErr := unreachableMgr.scoreWithSystemOne(ctx, "How do I configure postgres database indexes?", entries)
		require.Error(t, scoreErr, "Expected error when calling unreachable endpoint")
		require.True(t, errors.Is(scoreErr, systemone.ErrBackendFailed), "Expected ErrBackendFailed")
		require.Contains(t, scoreErr.Error(), "connection failed", "Expected descriptive error message about connection failure")
		t.Logf("Unreachable endpoint returned expected descriptive error: %v", scoreErr)
	})
}
