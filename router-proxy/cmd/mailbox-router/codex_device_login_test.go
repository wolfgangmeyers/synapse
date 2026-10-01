package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/codexdevice"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func TestCodexDeviceLoginIsolatedAndAtomic(t *testing.T) {
	root, err := os.MkdirTemp("/srv/disk-tmp", "codex-login-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	path := filepath.Join(root, "credential.json")
	db := filepath.Join(root, "router.db")
	prior := store.CodexCredential{AccessToken: "old-access", RefreshToken: "old-refresh", AccountID: "old-account", ExpiresAt: time.Now().Add(time.Hour)}
	valid := store.CodexCredential{AccessToken: "fake-access-private", RefreshToken: "fake-refresh-private", AccountID: "fake-account", ExpiresAt: time.Now().Add(time.Hour)}
	st, err := store.OpenLocalCodexCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(context.Background(), prior); err != nil {
		t.Fatal(err)
	}
	const hostile = "FAKE_PRIVATE_ISSUER_TOKEN_BODY_VERIFIER"
	run := func(t *testing.T, args []string, fake deviceAcquireFunc) (string, string, error) {
		t.Helper()
		var out, diagnostics bytes.Buffer
		err := runCodexDeviceLogin(context.Background(), args, &out, &diagnostics, fake)
		combined := out.String() + diagnostics.String()
		for _, secret := range []string{hostile, "fake-access-private", "fake-refresh-private", "fake-protocol-refresh-private", "fake-device-secret", "fake-verifier", "old-access", "old-refresh"} {
			if strings.Contains(combined, secret) || (err != nil && strings.Contains(err.Error(), secret)) {
				t.Fatalf("secret escaped: %q", secret)
			}
		}
		if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("router DB side effect: %v", err)
		}
		if _, err := os.Stat(db + ".attachments"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("router attachments side effect: %v", err)
		}
		return out.String(), diagnostics.String(), err
	}
	t.Run("fake_protocol_to_fresh_manager", func(t *testing.T) {
		// Same closed device-start, poll, and exchange fixture as codexdevice/device_test.go.
		// Only the test transport rewrites the fixed issuer to this loopback server.
		claim, err := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "fake-protocol-account"}})
		if err != nil {
			t.Fatal("synthetic claim encoding failed")
		}
		access := "fake." + base64.RawURLEncoding.EncodeToString(claim) + ".fake"
		const refresh = "fake-protocol-refresh-private"
		var polls, exchanges atomic.Int32
		var reject atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Error("unexpected fake issuer method")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			switch r.URL.Path {
			case "/api/accounts/deviceauth/usercode":
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["client_id"] != "app_EMoamEEZ73f0CkXaXp7hrann" {
					t.Error("invalid device start")
				}
				fmt.Fprint(w, `{"device_auth_id":"fake-device-secret","user_code":"FAKE-PUBLIC","interval":0}`)
			case "/api/accounts/deviceauth/token":
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 || body["device_auth_id"] != "fake-device-secret" || body["user_code"] != "FAKE-PUBLIC" {
					t.Error("invalid device poll")
				}
				if got := polls.Add(1); got > 2 || (got == 2) != reject.Load() {
					t.Error("unexpected extra poll")
				}
				fmt.Fprint(w, `{"authorization_code":"fake-code","code_verifier":"fake-verifier"}`)
			case "/oauth/token":
				if r.ParseForm() != nil || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
					t.Error("invalid exchange form")
				}
				want := url.Values{"grant_type": {"authorization_code"}, "client_id": {"app_EMoamEEZ73f0CkXaXp7hrann"}, "code": {"fake-code"}, "code_verifier": {"fake-verifier"}, "redirect_uri": {"https://auth.openai.com/deviceauth/callback"}}
				if r.Form.Encode() != want.Encode() {
					t.Error("invalid code exchange")
				}
				exchanges.Add(1)
				if reject.Load() {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, hostile)
					return
				}
				fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":3600}`, access, refresh)
			default:
				t.Error("unexpected fake issuer path")
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()
		issuer, err := url.Parse(server.URL)
		if err != nil {
			t.Fatal("invalid loopback issuer")
		}
		client := server.Client()
		client.Transport = deviceLoopbackTransport{issuer: issuer, inner: client.Transport}
		acquire := func(ctx context.Context, notify func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
			return codexdevice.Acquire(ctx, client, notify)
		}
		out, diag, err := run(t, []string{path}, acquire)
		if err != nil || out != "https://auth.openai.com/codex/device\nFAKE-PUBLIC\n" || diag != "" || strings.Contains(out+diag, access) {
			t.Fatal("closed fake acquisition failed or public output changed")
		}
		if polls.Load() != 1 || exchanges.Load() != 1 {
			t.Fatal("fake issuer sequence incomplete")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatal("credential file is not owner-only")
		}
		fresh, err := store.OpenLocalCodexCredentialStore(path)
		if err != nil {
			t.Fatal("cannot reopen private credential")
		}
		loaded, err := fresh.Load(context.Background())
		if err != nil || loaded.AccessToken != access || loaded.RefreshToken != refresh || loaded.AccountID != "fake-protocol-account" || !loaded.ExpiresAt.After(time.Now().Add(59*time.Minute)) {
			t.Fatal("atomic store did not retain exact synthetic credential")
		}
		options, err := parseConfig([]string{"--database", db, "--codex-credential-path", path})
		if err != nil {
			t.Fatal("cannot configure fresh manager")
		}
		managerOptions := managed.ManagerOptions{}
		if err := configureCodexCredentials(context.Background(), options, &managerOptions); err != nil || managerOptions.CodexCredentialStore == nil || managerOptions.CodexRefresh == nil {
			t.Fatal("fresh manager did not load credential path")
		}
		managedCredential, err := managerOptions.CodexCredentialStore.Load(context.Background())
		if err != nil || managedCredential.AccessToken != access || managedCredential.RefreshToken != refresh || managedCredential.AccountID != "fake-protocol-account" || !managedCredential.ExpiresAt.Equal(loaded.ExpiresAt) {
			t.Fatal("fresh manager boundary credential mismatch")
		}
		reject.Store(true)
		out, diag, err = run(t, []string{path}, acquire)
		if err == nil || diag != "" || out != "https://auth.openai.com/codex/device\nFAKE-PUBLIC\n" || strings.Contains(out+diag+err.Error(), access) || polls.Load() != 2 || exchanges.Load() != 2 {
			t.Fatal("failed code exchange did not follow closed fake path")
		}
		fresh, err = store.OpenLocalCodexCredentialStore(path)
		if err != nil {
			t.Fatal("cannot reopen credential after failed acquisition")
		}
		preserved, err := fresh.Load(context.Background())
		if err != nil || preserved != loaded {
			t.Fatal("failed acquisition replaced private credential")
		}
		if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("credential copied into SQLite")
		}
	})
	fake := func(_ context.Context, notify func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
		notify(codexdevice.DevicePrompt{UserCode: "FAKE-PUBLIC", VerificationURL: "https://" + hostile})
		return valid, nil
	}
	out, diag, err := run(t, []string{path}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if out != "https://auth.openai.com/codex/device\nFAKE-PUBLIC\n" || diag != "" {
		t.Fatalf("unsafe prompt: stdout=%q stderr=%q", out, diag)
	}
	reopened, err := store.OpenLocalCodexCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load(context.Background())
	if err != nil || got.AccessToken != valid.AccessToken || got.AccountID != valid.AccountID || got.RefreshToken != valid.RefreshToken {
		t.Fatalf("saved credential mismatch: err=%v", err)
	}
	for _, tc := range []struct {
		name    string
		acquire deviceAcquireFunc
	}{
		{"acquisition_error", func(_ context.Context, notify func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
			notify(codexdevice.DevicePrompt{UserCode: "FAKE-PUBLIC"})
			return prior, errors.New(hostile)
		}},
		{"cancel", func(ctx context.Context, notify func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
			notify(codexdevice.DevicePrompt{UserCode: "FAKE-PUBLIC"})
			return store.CodexCredential{}, context.Canceled
		}},
		{"missing_account", func(_ context.Context, notify func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
			notify(codexdevice.DevicePrompt{UserCode: "FAKE-PUBLIC"})
			v := prior
			v.AccountID = ""
			return v, nil
		}},
		{"expired", func(_ context.Context, notify func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
			notify(codexdevice.DevicePrompt{UserCode: "FAKE-PUBLIC"})
			v := prior
			v.ExpiresAt = time.Time{}
			return v, nil
		}},
		{"bad_code", func(_ context.Context, notify func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
			notify(codexdevice.DevicePrompt{UserCode: "FAKE-PUBLIC\n" + hostile})
			return prior, nil
		}},
		{"huge_code", func(_ context.Context, notify func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
			notify(codexdevice.DevicePrompt{UserCode: strings.Repeat("A", 256)})
			return prior, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := run(t, []string{path}, tc.acquire)
			if err == nil {
				t.Fatal("expected failure")
			}
			if tc.name == "bad_code" || tc.name == "huge_code" {
				if out != "" {
					t.Fatalf("printed bad code: %q", out)
				}
			}
			reopened, err := store.OpenLocalCodexCredentialStore(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reopened.Load(context.Background())
			if err != nil || got.AccessToken != valid.AccessToken || got.RefreshToken != valid.RefreshToken {
				t.Fatalf("failed login replaced credential: %v", err)
			}
		})
	}
	called := false
	_, _, err = run(t, []string{path, "--database", db}, func(_ context.Context, _ func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
		called = true
		return valid, nil
	})
	if err == nil || called {
		t.Fatal("unexpected flags reached issuer")
	}
	_, _, err = run(t, []string{root + "/../" + filepath.Base(root) + "/other.json"}, func(_ context.Context, _ func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
		called = true
		return valid, nil
	})
	if err == nil || called {
		t.Fatal("unclean path reached issuer")
	}
}

// Deny every destination except the fixed issuer, then send it to httptest.
// The inner transport sees only the loopback URL.
type deviceLoopbackTransport struct {
	issuer *url.URL
	inner  http.RoundTripper
}

func (d deviceLoopbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "auth.openai.com" || req.URL.User != nil || req.URL.RawQuery != "" || req.Method != http.MethodPost {
		return nil, errors.New("unexpected fake issuer destination")
	}
	copy := req.Clone(req.Context())
	copy.URL.Scheme = d.issuer.Scheme
	copy.URL.Host = d.issuer.Host
	copy.Host = d.issuer.Host
	return d.inner.RoundTrip(copy)
}
