//go:build embedded_restart_test_only

package main

// CC-007 build-tagged test transport. Wiring receives actual immutable
// binding/canonical-effect observations outside SQL transactions.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type claudeEffectBarrierReceipt struct {
	Phase      string `json:"phase"`
	AgentID    string `json:"agent_id"`
	SessionID  string `json:"session_id"`
	Epoch      int64  `json:"epoch"`
	Generation string `json:"generation"`
	OwnerToken int64  `json:"owner_token"`
	EffectID   string `json:"effect_id"`
}

type claudeEffectBarrier struct {
	endpoint, token string
	client          *http.Client
}

// Construction requires a second explicit opt-in in addition to the build tag.
// The fixture owns the listener; no router control/recovery authority is exposed.
func newClaudeEffectBarrier(enabled, endpoint, token string) (*claudeEffectBarrier, error) {
	if enabled != "1" {
		return nil, errors.New("Claude effect barrier requires explicit opt-in")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("barrier requires bare literal loopback HTTP URL")
	}
	host, port := u.Hostname(), u.Port()
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || (host != "127.0.0.1" && host != "::1") || endpoint != "http://"+net.JoinHostPort(host, port) || len(token) != 64 {
		return nil, errors.New("invalid barrier endpoint/token")
	}
	for _, ch := range token {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return nil, errors.New("invalid barrier token")
		}
	}
	address := net.JoinHostPort(host, port)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" || addr != address {
			return nil, errors.New("barrier dial denied")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, addr)
	}}
	return &claudeEffectBarrier{endpoint: endpoint, token: token, client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Caller MUST propagate failure to stop execution. Timeout is not a release.
func (b *claudeEffectBarrier) wait(ctx context.Context, receipt claudeEffectBarrierReceipt) error {
	if receipt.Phase != "intent" && receipt.Phase != "domain" && receipt.Phase != "checked" {
		return errors.New("invalid barrier phase")
	}
	if receipt.AgentID == "" || receipt.SessionID == "" || receipt.Epoch <= 0 || receipt.Generation == "" || receipt.OwnerToken <= 0 || receipt.EffectID == "" {
		return errors.New("barrier requires actual binding/effect")
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if len(raw) > 4096 {
		return errors.New("barrier receipt exceeds limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint+"/barrier", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("barrier not released: HTTP %d", response.StatusCode)
	}
	return nil
}
