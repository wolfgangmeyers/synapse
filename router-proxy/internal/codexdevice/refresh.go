package codexdevice

import (
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

// Refresh exchanges a stored refresh token with the fixed production issuer.
// It returns a complete replacement credential or an error; it never writes to the store.
func Refresh(ctx context.Context, client *http.Client, old store.CodexCredential) (store.CodexCredential, error) {
	return refresh(ctx, client, issuer, old)
}

// refresh's base override is private and used only with local fake issuer tests.
func refresh(ctx context.Context, client *http.Client, base string, old store.CodexCredential) (store.CodexCredential, error) {
	var empty store.CodexCredential
	if ctx == nil || client == nil || old.RefreshToken == "" {
		return empty, errors.New("refresh input invalid")
	}
	if ctx.Err() != nil {
		return empty, errors.New("refresh cancelled")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {old.RefreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return empty, errors.New("refresh request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := issuerClient(client).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return empty, errors.New("refresh cancelled")
		}
		return empty, errors.New("refresh request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if ctx.Err() != nil {
		return empty, errors.New("refresh cancelled")
	}
	if err != nil || len(body) > maxBody {
		return empty, errors.New("refresh response invalid")
	}
	if resp.StatusCode != http.StatusOK {
		return empty, errors.New("refresh rejected")
	}
	var token struct {
		Access  string      `json:"access_token"`
		Refresh string      `json:"refresh_token"`
		Expires json.Number `json:"expires_in"`
	}
	if json.Unmarshal(body, &token) != nil || token.Access == "" || token.Refresh == "" || len(token.Access) > maxBody/2 || len(token.Refresh) > maxBody/2 {
		return empty, errors.New("refresh credential invalid")
	}
	seconds, err := strconv.ParseFloat(string(token.Expires), 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds >= math.MaxInt64/float64(time.Second) {
		return empty, errors.New("refresh credential invalid")
	}
	duration := time.Duration(seconds * float64(time.Second))
	if duration <= 0 {
		return empty, errors.New("refresh credential invalid")
	}
	parts := strings.Split(token.Access, ".")
	if len(parts) != 3 {
		return empty, errors.New("refresh credential invalid")
	}
	claim, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return empty, errors.New("refresh credential invalid")
	}
	var jwt map[string]json.RawMessage
	var auth struct {
		AccountID string `json:"chatgpt_account_id"`
	}
	if json.Unmarshal(claim, &jwt) != nil || json.Unmarshal(jwt["https://api.openai.com/auth"], &auth) != nil || auth.AccountID == "" || len(auth.AccountID) > 4096 {
		return empty, errors.New("refresh credential invalid")
	}
	if ctx.Err() != nil {
		return empty, errors.New("refresh cancelled")
	}
	return store.CodexCredential{AccessToken: token.Access, RefreshToken: token.Refresh, ExpiresAt: time.Now().Add(duration), AccountID: auth.AccountID}, nil
}
