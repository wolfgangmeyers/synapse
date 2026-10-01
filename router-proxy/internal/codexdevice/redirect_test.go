package codexdevice

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func TestDeviceRedirectDoesNotSendToHostileRecipient(t *testing.T) {
	for _, tc := range []struct {
		name, path, wantError string
		status                int
	}{
		{"start_302", "/api/accounts/deviceauth/usercode", "device request failed", http.StatusFound},
		{"start_307", "/api/accounts/deviceauth/usercode", "device request failed", http.StatusTemporaryRedirect},
		{"poll_307", "/api/accounts/deviceauth/token", "device request failed", http.StatusTemporaryRedirect},
		{"exchange_302", "/oauth/token", "device request failed", http.StatusFound},
		{"exchange_307", "/oauth/token", "device request failed", http.StatusTemporaryRedirect},
		{"exchange_308", "/oauth/token", "device request failed", http.StatusPermanentRedirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hostileRequests atomic.Int32
			hostile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hostileRequests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"access_token":"fake-hostile","refresh_token":"fake-hostile","expires_in":3600}`)
			}))
			defer hostile.Close()
			var polls atomic.Int32
			issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.path {
					w.Header().Set("Location", hostile.URL+"/capture")
					w.WriteHeader(tc.status)
					return
				}
				switch r.URL.Path {
				case "/api/accounts/deviceauth/usercode":
					_, _ = io.WriteString(w, `{"device_auth_id":"fake-device-private","user_code":"FAKE-PUBLIC","interval":0}`)
				case "/api/accounts/deviceauth/token":
					polls.Add(1)
					_, _ = io.WriteString(w, `{"authorization_code":"fake-code-private","code_verifier":"fake-verifier-private"}`)
				default:
					t.Errorf("unexpected issuer path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer issuer.Close()
			client := issuer.Client() // Its default policy follows redirects.
			if client.CheckRedirect != nil {
				t.Fatal("fixture client does not have default redirect policy")
			}
			got, err := acquire(context.Background(), client, issuer.URL, func(DevicePrompt) {})
			if err == nil || err.Error() != tc.wantError || got != (store.CodexCredential{}) {
				t.Errorf("unexpected result: credential=%+v err=%v", got, err)
			}
			for _, secret := range []string{"fake-code-private", "fake-verifier-private", "fake-device-private", "fake-hostile"} {
				if err != nil && strings.Contains(err.Error(), secret) {
					t.Errorf("error disclosed %s", secret)
				}
			}
			if n := hostileRequests.Load(); n != 0 {
				t.Errorf("hostile received %d requests", n)
			}
			if client.CheckRedirect != nil {
				t.Error("caller client mutated")
			}
			if (tc.path == "/oauth/token" && polls.Load() != 1) || (tc.path == "/api/accounts/deviceauth/token" && polls.Load() != 0) {
				t.Errorf("unexpected completed polls: %d", polls.Load())
			}
		})
	}
}

func TestRefreshRedirectDoesNotSendToHostileRecipient(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var hostileRequests atomic.Int32
			hostile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hostileRequests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"access_token":"fake-hostile","refresh_token":"fake-hostile","expires_in":3600}`)
			}))
			defer hostile.Close()
			issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/oauth/token" {
					t.Errorf("unexpected issuer path %s", r.URL.Path)
				}
				w.Header().Set("Location", hostile.URL+"/capture")
				w.WriteHeader(status)
			}))
			defer issuer.Close()
			client := issuer.Client()
			if client.CheckRedirect != nil {
				t.Fatal("fixture client does not have default redirect policy")
			}
			got, err := refresh(context.Background(), client, issuer.URL, store.CodexCredential{RefreshToken: "fake-refresh-private"})
			if err == nil || err.Error() != "refresh request failed" || got != (store.CodexCredential{}) {
				t.Errorf("unexpected result: credential=%+v err=%v", got, err)
			}
			if err != nil && (strings.Contains(err.Error(), "fake-refresh-private") || strings.Contains(err.Error(), "fake-hostile")) {
				t.Error("error disclosed credential")
			}
			if n := hostileRequests.Load(); n != 0 {
				t.Errorf("hostile received %d requests", n)
			}
			if client.CheckRedirect != nil {
				t.Error("caller client mutated")
			}
		})
	}
}
