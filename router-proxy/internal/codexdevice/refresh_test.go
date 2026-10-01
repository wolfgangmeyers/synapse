package codexdevice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func TestRefreshRotatesCompleteCredential(t *testing.T) {
	old := store.CodexCredential{AccessToken: "old-access-private", RefreshToken: "old-refresh-private", AccountID: "old-account-private", ExpiresAt: time.Now().Add(-time.Hour)}
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if r.Method != http.MethodPost || r.URL.Path != "/oauth/token" || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Error("wrong refresh request metadata")
		}
		if err := r.ParseForm(); err != nil {
			t.Error("invalid refresh form")
		}
		want := url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {old.RefreshToken}}
		if r.PostForm.Encode() != want.Encode() || len(r.PostForm) != 3 {
			t.Error("wrong refresh form")
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"new-refresh-private","expires_in":3600}`, fakeJWT("new-account"))
	}))
	defer srv.Close()
	before := time.Now()
	got, err := refresh(context.Background(), srv.Client(), srv.URL, old)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !seen || got.AccessToken != fakeJWT("new-account") || got.RefreshToken != "new-refresh-private" || got.AccountID != "new-account" || got.ExpiresAt.Before(before.Add(3599*time.Second)) || got.ExpiresAt.After(time.Now().Add(3601*time.Second)) {
		t.Fatal("incomplete rotated credential")
	}
	if old.AccessToken != "old-access-private" || old.RefreshToken != "old-refresh-private" || old.AccountID != "old-account-private" {
		t.Fatal("input credential mutated")
	}
}

func TestRefreshRejectsIncompleteAndHostileIssuer(t *testing.T) {
	old := store.CodexCredential{AccessToken: "old-access-private", RefreshToken: "old-refresh-private", AccountID: "old-account-private"}
	for _, tc := range []struct {
		name, body, wantError string
		status                int
	}{
		{"rejected", hostile, "refresh rejected", 401},
		{"malformed", hostile, "refresh credential invalid", 200},
		{"missing_access", `{"refresh_token":"rotated","expires_in":3600}`, "refresh credential invalid", 200},
		{"missing_refresh", fmt.Sprintf(`{"access_token":%q,"expires_in":3600}`, fakeJWT("new-account")), "refresh credential invalid", 200},
		{"missing_claim", fmt.Sprintf(`{"access_token":%q,"refresh_token":"rotated","expires_in":3600}`, "fake.e30.fake"), "refresh credential invalid", 200},
		{"zero_expiry", fmt.Sprintf(`{"access_token":%q,"refresh_token":"rotated","expires_in":0}`, fakeJWT("new-account")), "refresh credential invalid", 200},
		{"negative_expiry", fmt.Sprintf(`{"access_token":%q,"refresh_token":"rotated","expires_in":-1}`, fakeJWT("new-account")), "refresh credential invalid", 200},
		{"huge_expiry", fmt.Sprintf(`{"access_token":%q,"refresh_token":"rotated","expires_in":1e100}`, fakeJWT("new-account")), "refresh credential invalid", 200},
		{"oversized", strings.Repeat(hostile, maxBody), "refresh response invalid", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got, err := refresh(context.Background(), srv.Client(), srv.URL, old)
			if got != (store.CodexCredential{}) || err == nil || err.Error() != tc.wantError || strings.Contains(err.Error(), hostile) || strings.Contains(err.Error(), old.RefreshToken) {
				t.Fatal("refresh failure leaked data or returned partial credential")
			}
		})
	}
}

func TestRefreshCancelledAndTransportFailure(t *testing.T) {
	old := store.CodexCredential{RefreshToken: "old-refresh-private"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := refresh(ctx, http.DefaultClient, "http://127.0.0.1:1", old)
	if got != (store.CodexCredential{}) || err == nil || err.Error() != "refresh cancelled" {
		t.Fatal("cancellation returned credential or unclassified error")
	}
	release := make(chan struct{})
	var releaseOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		releaseOnce.Do(func() { close(release) })
		srv.Close()
	}()
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	got, err = refresh(ctx, srv.Client(), srv.URL, old)
	if got != (store.CodexCredential{}) || err == nil || err.Error() != "refresh cancelled" {
		t.Fatal("in-flight cancellation returned credential or unclassified error")
	}
	releaseOnce.Do(func() { close(release) })
	client := &http.Client{Transport: roundTripError{}}
	got, err = refresh(context.Background(), client, srv.URL, old)
	if got != (store.CodexCredential{}) || err == nil || err.Error() != "refresh request failed" || strings.Contains(err.Error(), hostile) {
		t.Fatal("transport failure returned credential or unclassified error")
	}
}

type roundTripError struct{}

func (roundTripError) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New(hostile)
}
