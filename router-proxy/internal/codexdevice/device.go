// Package codexdevice acquires a Codex credential using the device-code flow.
// It does not persist credentials; callers may save only a successful result.
package codexdevice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	clientID        = "app_EMoamEEZ73f0CkXaXp7hrann"
	issuer          = "https://auth.openai.com"
	verificationURL = "https://auth.openai.com/codex/device"
	redirectURI     = "https://auth.openai.com/deviceauth/callback"
	maxBody         = 64 << 10
)

// DevicePrompt contains only information safe to show to the user.
type DevicePrompt struct {
	UserCode        string
	VerificationURL string
	Interval        time.Duration
	ExpiresIn       time.Duration
}

// ValidUserCode admits only the short display code, never an issuer token.
func ValidUserCode(code string) bool {
	if len(code) < 1 || len(code) > 32 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if c == '-' && i > 0 && i < len(code)-1 {
			continue
		}
		return false
	}
	return true
}

// Acquire starts a production device flow. No issuer override is exposed to callers.
// notify is called once before polling; do not log the returned credential.
func Acquire(ctx context.Context, client *http.Client, notify func(DevicePrompt)) (store.CodexCredential, error) {
	return acquire(ctx, client, issuer, notify)
}

// issuerClient keeps credentials on the configured issuer even when it redirects.
// Copy the client so callers retain their own redirect policy for other requests.
func issuerClient(client *http.Client) *http.Client {
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("issuer redirect refused") }
	return &copy
}

func post(ctx context.Context, client *http.Client, endpoint, contentType string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, errors.New("device request failed")
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := issuerClient(client).Do(req)
	if err != nil {
		return 0, nil, errors.New("device request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(data) > maxBody {
		return 0, nil, errors.New("device response invalid")
	}
	return resp.StatusCode, data, nil
}

func acquire(ctx context.Context, client *http.Client, base string, notify func(DevicePrompt)) (store.CodexCredential, error) {
	var empty store.CodexCredential
	if ctx == nil || client == nil || notify == nil {
		return empty, errors.New("device flow invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 900*time.Second)
	defer cancel()
	status, body, err := post(ctx, client, base+"/api/accounts/deviceauth/usercode", "application/json", []byte(`{"client_id":"`+clientID+`"}`))
	if err != nil {
		return empty, err
	}
	if status != 200 {
		return empty, errors.New("device start failed")
	}
	var start struct {
		DeviceAuthID string      `json:"device_auth_id"`
		UserCode     string      `json:"user_code"`
		Interval     json.Number `json:"interval"`
	}
	if json.Unmarshal(body, &start) != nil || len(start.DeviceAuthID) == 0 || len(start.DeviceAuthID) > 4096 || len(start.UserCode) == 0 || len(start.UserCode) > 256 {
		return empty, errors.New("device start invalid")
	}
	interval := time.Second * 5
	if start.Interval != "" {
		n, e := strconv.ParseFloat(string(start.Interval), 64)
		if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 900 {
			return empty, errors.New("device start invalid")
		}
		interval = time.Duration(n * float64(time.Second))
	}
	if interval < time.Second {
		interval = time.Second
	}
	notify(DevicePrompt{UserCode: start.UserCode, VerificationURL: verificationURL, Interval: interval, ExpiresIn: 900 * time.Second})
	if err := ctx.Err(); err != nil {
		return empty, errors.New("device flow cancelled")
	}
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return empty, errors.New("device flow cancelled or expired")
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return empty, errors.New("device flow cancelled or expired")
		}
		payload, _ := json.Marshal(map[string]string{"device_auth_id": start.DeviceAuthID, "user_code": start.UserCode})
		status, body, err = post(ctx, client, base+"/api/accounts/deviceauth/token", "application/json", payload)
		if err != nil {
			return empty, err
		}
		if status == 200 {
			break
		}
		if status != 403 && status != 404 {
			var failure struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			_ = json.Unmarshal(body, &failure)
			switch failure.Error.Code {
			case "deviceauth_authorization_pending":
			case "slow_down":
				interval += 5 * time.Second
			default:
				return empty, errors.New("device authorization failed")
			}
		}
	}
	var code struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if json.Unmarshal(body, &code) != nil || len(code.AuthorizationCode) == 0 || len(code.AuthorizationCode) > 4096 || len(code.CodeVerifier) == 0 || len(code.CodeVerifier) > 4096 {
		return empty, errors.New("device authorization invalid")
	}
	if ctx.Err() != nil {
		return empty, errors.New("device flow cancelled")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code.AuthorizationCode}, "code_verifier": {code.CodeVerifier}, "redirect_uri": {redirectURI}}
	status, body, err = post(ctx, client, base+"/oauth/token", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return empty, err
	}
	if status != 200 {
		return empty, errors.New("device exchange failed")
	}
	var token struct {
		Access  string      `json:"access_token"`
		Refresh string      `json:"refresh_token"`
		Expires json.Number `json:"expires_in"`
	}
	if json.Unmarshal(body, &token) != nil || len(token.Access) == 0 || len(token.Refresh) == 0 || len(token.Access) > maxBody/2 || len(token.Refresh) > maxBody/2 {
		return empty, errors.New("device exchange invalid")
	}
	seconds, e := strconv.ParseFloat(string(token.Expires), 64)
	if e != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > math.MaxInt64/float64(time.Second) {
		return empty, errors.New("device exchange invalid")
	}
	parts := strings.Split(token.Access, ".")
	if len(parts) != 3 {
		return empty, errors.New("device exchange invalid")
	}
	claim, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return empty, errors.New("device exchange invalid")
	}
	var jwt map[string]json.RawMessage
	var auth struct {
		AccountID string `json:"chatgpt_account_id"`
	}
	if json.Unmarshal(claim, &jwt) != nil || json.Unmarshal(jwt["https://api.openai.com/auth"], &auth) != nil || auth.AccountID == "" || len(auth.AccountID) > 4096 || ctx.Err() != nil {
		return empty, errors.New("device exchange invalid")
	}
	return store.CodexCredential{AccessToken: token.Access, RefreshToken: token.Refresh, ExpiresAt: time.Now().Add(time.Duration(seconds * float64(time.Second))), AccountID: auth.AccountID}, nil
}
