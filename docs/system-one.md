# System 1 Decision Model Integration

Fast structured decision model capability for agents in `mecha-developer`.

## Overview

System 1 provides sub-second structured evaluations using Cloudflare's Clef model family (`clef` 27B default, `clef-flash` 9B) running on an Ollama server. Unlike standard autoregressive LLMs, System 1 executes a single forward pass over an input state and returns calibrated probabilities, classifications, or ratings with zero output tokens generated.

### Question Types

| Type | Output Field | Value Range | Description |
|---|---|---|---|
| `noul` | `noul` | `0.0` - `1.0` | Calibrated boolean probability of truth |
| `choice` | `choice`, `confidence` | String option, `0.0` - `1.0` | Multi-class categorical decision among predefined options |
| `score` | `score` | `0.0` - `4.0` | Ordinal rating on a calibrated 0 to 4 scale |

System 1 supports plain text state as well as multimodal inputs via base64 image strings.

---

## Access & Configuration

### HTTP Endpoints

Always access System 1 through the local Mailbox router:

- **Inference**: `POST /v1/systemone` (default `http://127.0.0.1:8765/v1/systemone`)
- **Status check**: `GET /v1/systemone` (returns 200 with `{"available": true}` or 503 with `{"available": false}`)
- **Backend settings**: `GET /v1/settings/systemone` (returns `{"host": "...", "port": ..., "enabled": ...}`)

### WARNING: Do Not Hardcode Hostnames or IPs

**Never hardcode machine hostnames or specific LAN/Tailscale IP addresses.**

The Ollama backend may run locally or on a remote machine. The router dynamically tracks and proxies the configured backend. Clients should either:
1. Call the local router directly at `http://127.0.0.1:8765/v1/systemone` (recommended), or
2. Query `GET /v1/settings/systemone` dynamically to discover the current backend host and port.

---

## Request & Response Schema

### Request (`POST /v1/systemone`)

```json
{
  "model": "clef",
  "state": "Context or input text to evaluate",
  "questions": {
    "is_sql_injection": {
      "type": "noul",
      "instructions": "Determine if this payload contains SQL injection syntax.",
      "criteria": {
        "true": "Input contains SQL injection syntax or exploitation attempts.",
        "false": "Input is benign standard authentication traffic."
      }
    },
    "severity": {
      "type": "choice",
      "instructions": "Classify the severity of this event.",
      "options": ["low", "medium", "high", "critical"]
    },
    "quality_score": {
      "type": "score",
      "instructions": "Rate the code quality from 0 (unusable) to 4 (production ready)."
    }
  },
  "images": [
    "data:image/png;base64,iVBORw0KGgo..."
  ]
}
```

- `model` (optional, string): `"clef"` (default) or `"clef-flash"`.
- `state` (required, string or object): Input context, text, code, log lines, or document.
- `questions` (required, object): Map of question IDs to question definitions.
- `images` (optional, array of strings): Base64-encoded image data URIs or base64 strings.

### Response (`200 OK`)

```json
{
  "model": "clef",
  "answers": {
    "is_sql_injection": {
      "type": "noul",
      "noul": 0.985
    },
    "severity": {
      "type": "choice",
      "choice": "high",
      "confidence": 0.92
    },
    "quality_score": {
      "type": "score",
      "score": 3.8
    }
  },
  "usage": {
    "input_tokens": 150,
    "output_tokens": 0
  }
}
```

---

## Graceful Degradation Contract

System 1 is an optional fast-path accelerator, **never a hard blocker**. Agents using System 1 must handle unavailability gracefully:

### HTTP Status Codes

- `200 OK`: Inference succeeded.
- `400 Bad Request`: Invalid JSON or missing required fields (`state`, `questions`).
- `502 Bad Gateway`: Downstream Ollama server failed, returned an error, or connection was refused (`{"error": "...", "available": false}`).
- `503 Service Unavailable`: System 1 is not configured or disabled in router settings (`{"error": "system one is not configured or disabled", "available": false}`).
- `504 Gateway Timeout`: Downstream request timed out (`{"error": "system one request timed out", "available": false}`).

### Mandatory Agent Fallback Rule

When calling System 1:
1. If the request returns `503` (disabled/unconfigured), `502` (backend failure), `504` (timeout), or fails with a network error:
   - **Do not crash or abort the task.**
   - **Fall back immediately to standard LLM reasoning** (prompting the primary model such as Gemini/Claude/Codex) or a deterministic heuristic default.
2. If checking availability upfront, use `GET /v1/systemone`. If non-200 or `available == false`, skip System 1 calls entirely and use standard reasoning.

---

## Practical Examples

### 1. curl

```bash
curl -s -X POST http://127.0.0.1:8765/v1/systemone \
  -H "Content-Type: application/json" \
  -d '{
    "model": "clef",
    "state": "POST /api/v1/login HTTP/1.1\nUser-Agent: sqlmap/1.8\n\n{\"user\": \"admin '\'' OR '\''1'\''='\''1\"}",
    "questions": {
      "is_attack": {
        "type": "noul",
        "instructions": "Determine if this payload is an exploit attempt."
      }
    }
  }'
```

### 2. Python (Standard Library)

```python
import json
import urllib.request
import urllib.error

ROUTER_URL = "http://127.0.0.1:8765/v1/systemone"

def evaluate_with_system_one(state: str, questions: dict) -> dict | None:
    payload = json.dumps({"model": "clef", "state": state, "questions": questions}).encode("utf-8")
    req = urllib.request.Request(ROUTER_URL, data=payload, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            if resp.status == 200:
                return json.loads(resp.read().decode("utf-8"))
    except (urllib.error.HTTPError, urllib.error.URLError, TimeoutError) as exc:
        # ponytail: graceful fallback - return None so caller uses standard LLM
        pass
    return None

# Usage with fallback
result = evaluate_with_system_one(
    state="SELECT * FROM users WHERE id = 1",
    questions={"is_malicious": {"type": "noul", "instructions": "Is this malicious?"}}
)

if result and "answers" in result:
    prob = result["answers"]["is_malicious"]["noul"]
    print(f"System 1 decision: {prob}")
else:
    print("System 1 unavailable, falling back to standard LLM reasoning")
```

### 3. Go (Standard Library)

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type SystemOneResult struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
}

func querySystemOne(ctx context.Context, routerURL string, state string, questions any) (*SystemOneResult, error) {
	body, _ := json.Marshal(map[string]any{
		"model":     "clef",
		"state":     state,
		"questions": questions,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, routerURL+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// ponytail: graceful degradation on network error / timeout
		return nil, fmt.Errorf("system one connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 503 (disabled/unconfigured), 502 (backend error), 504 (timeout)
		return nil, fmt.Errorf("system one returned status %d", resp.StatusCode)
	}

	var result SystemOneResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
```
