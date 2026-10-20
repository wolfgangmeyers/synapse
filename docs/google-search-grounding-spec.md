# Google Search Grounding API & Tool Specifications Reference

## 1. Local Implementation (`pi-antigravity`)
- Tool registered in `/home/wolfgang/.pi/agent/npm/node_modules/pi-antigravity/src/index.ts`
- Core payload builder in `/home/wolfgang/.pi/agent/npm/node_modules/pi-antigravity/src/search/search.ts`
- Client headers & credentials in `/home/wolfgang/.pi/agent/npm/node_modules/pi-antigravity/src/client/client.ts`
- Existing router auth helpers in `router-proxy/internal/managed/`:
  - `embedded_google_auth.go` (`m.embeddedGoogleAuthWithRefresh` for Antigravity)
  - `embedded_vertex_auth.go` (`m.embeddedVertexAuth` for Vertex AI)
  - `embedded_vertex_stream.go` (`streamEmbeddedVertex`)

### Antigravity Payload Construction
```typescript
const tools = [{ googleSearch: {} }];
if (options.urls?.length) tools.push({ urlContext: {} });

const request = {
  project: projectId,
  model: "gemini-3.8-flash", // fallbacks: gemini-3.8-flash-low, gemini-3.6-flash-low
  request: {
    systemInstruction: { role: "user", parts: [{ text: systemPrompt }] },
    contents: [{ role: "user", parts: [{ text: queryPrompt }] }],
    tools,
    generationConfig: {
      thinkingConfig: { thinkingBudget: 2048, includeThoughts: false }
    }
  },
  requestType: "agent",
  userAgent: "antigravity",
  requestId: envelope.requestId
};
```

## 2. API & Tool Specifications

### Wire Tool Schema
Field name is camelCase `googleSearch`:
```json
"tools": [
  { "googleSearch": {} }
]
```
Note: In Vertex AI, `excludeDomains` can be passed inside `googleSearch`. Empty `{}` performs standard Google Web Search.

### Antigravity vs Vertex AI
- **Antigravity Endpoint**: `POST https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent`
  - Header: `Authorization: Bearer <token>`, `User-Agent: antigravity/cli/1.2.4 (aidev_client; os_type=linux; arch=amd64; cl=982146307; auth_method=consumer)`
  - Body: Enveloped in `{ project, model, request, requestType: "agent", userAgent: "antigravity", requestId }`
- **Vertex AI Endpoint**: `POST https://{location}-aiplatform.googleapis.com/v1beta1/projects/{projectID}/locations/{location}/publishers/google/models/{model}:generateContent`
  - Header: `Authorization: Bearer <token>`
  - Body: Direct Gemini payload `{ contents: [...], tools: [{ "googleSearch": {} }], systemInstruction: {...} }`

### Response `groundingMetadata` Schema
Found in `candidates[0].groundingMetadata` (check `candidates[0]` or `response.candidates[0]`):
```json
{
  "candidates": [
    {
      "content": {
        "parts": [{ "text": "Synthesized text..." }],
        "role": "model"
      },
      "finishReason": "STOP",
      "groundingMetadata": {
        "webSearchQueries": ["query 1", "query 2"],
        "searchEntryPoint": {
          "renderedContent": "<style>...</style><div class=\"google-search-entry-point\">...</div>"
        },
        "groundingChunks": [
          {
            "web": {
              "uri": "https://example.com",
              "title": "Example Title"
            }
          }
        ],
        "groundingSupports": [
          {
            "segment": { "startIndex": 0, "endIndex": 50, "text": "..." },
            "groundingChunkIndices": [0],
            "confidenceScores": [0.95]
          }
        ]
      }
    }
  ]
}
```

## 3. Go Implementation for `router-proxy/internal/managed/`

```go
func (m *Manager) ExecuteGoogleSearch(ctx context.Context, query string) (string, error) {
	token, projectID, err := m.embeddedGoogleAuth(ctx)
	if err == nil && token != "" {
		return m.executeAntigravityGrounding(ctx, token, projectID, query)
	}
	vToken, vProject, vLoc, vErr := m.embeddedVertexAuth(ctx, false)
	if vErr == nil && vToken != "" {
		return m.executeVertexGrounding(ctx, vToken, vProject, vLoc, query)
	}
	return "", fmt.Errorf("no Google credentials available for search")
}
```
See full parser and client code in `router-proxy/internal/managed/`.
