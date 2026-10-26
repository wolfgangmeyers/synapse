package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

var (
	ErrNotConfigured = errors.New("system one is not configured")
	ErrDisabled      = errors.New("system one is disabled")
	ErrTimeout       = errors.New("system one request timed out")
	ErrBackendFailed = errors.New("system one backend call failed")
)

// Config describes the connection settings for System 1.
type Config struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Enabled bool   `json:"enabled"`
}

// IsConfigured returns true if System 1 is enabled and has valid host and port.
func (c Config) IsConfigured() bool {
	return c.Enabled && strings.TrimSpace(c.Host) != "" && c.Port > 0
}

// EndpointURL constructs the full HTTP endpoint URL for System 1 decision invocations.
func (c Config) EndpointURL() string {
	host := strings.TrimSpace(c.Host)
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	host = strings.TrimRight(host, "/")
	return fmt.Sprintf("%s:%d/v1/systemone", host, c.Port)
}

// Request is a structured payload for System 1 decision invocations.
type Request struct {
	Model     string   `json:"model,omitempty"`
	State     any      `json:"state"`
	Questions any      `json:"questions"`
	Images    []string `json:"images,omitempty"`
}

// Response wraps the decision response with typed accessors and the raw payload.
type Response struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers,omitempty"`
	Usage   map[string]any             `json:"usage,omitempty"`
	Raw     json.RawMessage            `json:"raw,omitempty"`
}

// RawAnswer returns the raw JSON for a given question name.
func (r *Response) RawAnswer(question string) []byte {
	if r.Answers == nil {
		return nil
	}
	return r.Answers[question]
}

// AnswerMap unmarshals a question's answer into a map.
func (r *Response) AnswerMap(question string) (map[string]any, error) {
	raw := r.RawAnswer(question)
	if len(raw) == 0 {
		return nil, fmt.Errorf("question %q not found in answers", question)
	}
	var res map[string]any
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res, nil
}

// Choice returns the chosen option string for a Choice question.
func (r *Response) Choice(question string) (string, error) {
	m, err := r.AnswerMap(question)
	if err != nil {
		return "", err
	}
	choice, ok := m["choice"].(string)
	if !ok {
		return "", fmt.Errorf("question %q does not have a choice string", question)
	}
	return choice, nil
}

// Noul returns the probability (0.0 to 1.0) for a Noul question.
func (r *Response) Noul(question string) (float64, error) {
	m, err := r.AnswerMap(question)
	if err != nil {
		return 0, err
	}
	noul, ok := m["noul"].(float64)
	if !ok {
		return 0, fmt.Errorf("question %q does not have a noul float", question)
	}
	return noul, nil
}

// Score returns the score value for a Score question.
func (r *Response) Score(question string) (float64, error) {
	m, err := r.AnswerMap(question)
	if err != nil {
		return 0, err
	}
	score, ok := m["score"].(float64)
	if !ok {
		return 0, fmt.Errorf("question %q does not have a score float", question)
	}
	return score, nil
}

// Confidence returns the confidence for a question if present.
func (r *Response) Confidence(question string) (float64, error) {
	m, err := r.AnswerMap(question)
	if err != nil {
		return 0, err
	}
	conf, ok := m["confidence"].(float64)
	if !ok {
		return 0, fmt.Errorf("question %q does not have confidence", question)
	}
	return conf, nil
}

// Client executes System 1 requests against configured servers.
type Client struct {
	HTTPClient *http.Client
}

// NewClient creates a new System 1 client.
func NewClient(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	return &Client{HTTPClient: client}
}

// Invoke executes a System 1 decision call.
// payload can be a *Request, Request, []byte, json.RawMessage, map[string]any, or any JSON-serializable value.
func (c *Client) Invoke(ctx context.Context, cfg Config, payload any) (*Response, error) {
	if !cfg.Enabled {
		return nil, ErrDisabled
	}
	if strings.TrimSpace(cfg.Host) == "" || cfg.Port <= 0 {
		return nil, ErrNotConfigured
	}

	var bodyBytes []byte
	switch v := payload.(type) {
	case []byte:
		bodyBytes = v
	case json.RawMessage:
		bodyBytes = v
	case string:
		bodyBytes = []byte(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal system one payload: %w", err)
		}
		bodyBytes = data
	}

	// Ensure model field defaults to "clef" if missing in JSON object
	var obj map[string]any
	if err := json.Unmarshal(bodyBytes, &obj); err == nil {
		if m, ok := obj["model"].(string); !ok || strings.TrimSpace(m) == "" {
			obj["model"] = "clef"
			if updated, err := json.Marshal(obj); err == nil {
				bodyBytes = updated
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.EndpointURL(), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create system one request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return nil, fmt.Errorf("%w: %v", ErrTimeout, err)
		}
		return nil, fmt.Errorf("%w: system one connection failed: %v", ErrBackendFailed, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read system one response: %w", err)
	}

	if resp.StatusCode == http.StatusGatewayTimeout || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("%w: system one returned status %d: %s", ErrTimeout, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: system one returned status %d: %s", ErrBackendFailed, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var parsed Response
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal system one response: %w", err)
	}
	parsed.Raw = json.RawMessage(respBody)

	return &parsed, nil
}
