package codexdevice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const hostile = "HOSTILE_PRIVATE_MARKER"

func fakeJWT(account string) string {
	payload, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account}})
	return "fake." + base64.RawURLEncoding.EncodeToString(payload) + ".fake"
}

// TestDeviceFirstPollWait checks the server-observed start-to-poll gap.
// The short caller deadline checks cancellation while waiting, not the full 900s cap.
func TestDeviceFirstPollWait(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cancel   bool
		deadline bool
		wantPoll bool
	}{
		{name: "minimum_one_second", wantPoll: true},
		{name: "cancel_during_first_wait", cancel: true},
		{name: "bounded_caller_deadline_during_first_wait", deadline: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var polls atomic.Int32
			startTimes := make(chan time.Time, 1)
			pollTimes := make(chan time.Time, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/accounts/deviceauth/usercode":
					startTimes <- time.Now()
					fmt.Fprint(w, `{"device_auth_id":"fake-device-secret","user_code":"FAKE-PUBLIC","interval":0}`)
				case "/api/accounts/deviceauth/token":
					pollTimes <- time.Now()
					polls.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			if tc.deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			}
			defer cancel()
			began := time.Now()
			_, err := acquire(ctx, srv.Client(), srv.URL, func(p DevicePrompt) {
				if p.Interval != time.Second {
					t.Errorf("minimum interval = %s, want 1s", p.Interval)
				}
				if tc.cancel {
					time.AfterFunc(50*time.Millisecond, cancel)
				}
			})
			if err == nil {
				t.Fatal("expected terminal or cancellation error")
			}
			start := <-startTimes
			if tc.wantPoll {
				if polls.Load() != 1 {
					t.Fatalf("polls = %d, want 1", polls.Load())
				}
				if elapsed := (<-pollTimes).Sub(start); elapsed < time.Second {
					t.Errorf("first poll %s after start, want at least 1s", elapsed)
				}
			} else {
				if polls.Load() != 0 {
					t.Errorf("polls during first wait = %d, want 0", polls.Load())
				}
				if elapsed := time.Since(began); elapsed >= time.Second {
					t.Errorf("cancellation took %s, want less than first interval", elapsed)
				}
			}
		})
	}
}

func TestFakeDeviceAcquisition(t *testing.T) {
	for _, tc := range []struct {
		name        string
		polls       []int
		tokenStatus int
		tokenBody   string
		cancel      bool
		wantSuccess bool
	}{
		{name: "pending403_404_slowdown_success", polls: []int{403, 404, 429, 200}, wantSuccess: true},
		{name: "terminal_hostile", polls: []int{500}},
		{name: "bad_exchange", polls: []int{200}, tokenBody: `{"access_token":"HOSTILE_PRIVATE_MARKER","refresh_token":"fake-refresh","expires_in":3600}`},
		{name: "cancel", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var polls atomic.Int32
			var seenCode string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					t.Errorf("method %s", r.Method)
					w.WriteHeader(400)
					return
				}
				switch r.URL.Path {
				case "/api/accounts/deviceauth/usercode":
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if len(body) != 1 || body["client_id"] != clientID {
						t.Errorf("start payload: %#v", body)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"device_auth_id":"fake-device-secret","user_code":"FAKE-PUBLIC","interval":0}`)
				case "/api/accounts/deviceauth/token":
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if len(body) != 2 || body["device_auth_id"] != "fake-device-secret" || body["user_code"] != "FAKE-PUBLIC" {
						t.Errorf("poll payload: %#v", body)
					}
					idx := int(polls.Add(1)) - 1
					if idx >= len(tc.polls) {
						t.Error("unexpected extra poll")
						w.WriteHeader(500)
						return
					}
					status := tc.polls[idx]
					w.WriteHeader(status)
					switch status {
					case 200:
						fmt.Fprint(w, `{"authorization_code":"fake-code","code_verifier":"fake-verifier"}`)
					case 429:
						fmt.Fprint(w, `{"error":{"code":"slow_down"}}`)
					case 500:
						fmt.Fprint(w, hostile)
					}
				case "/oauth/token":
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
						t.Error("exchange content type")
					}
					expected := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {"fake-code"}, "code_verifier": {"fake-verifier"}, "redirect_uri": {"https://auth.openai.com/deviceauth/callback"}}
					if r.Form.Encode() != expected.Encode() {
						t.Errorf("exchange payload: %v", r.Form)
					}
					seenCode = r.Form.Get("code")
					if tc.tokenStatus != 0 {
						w.WriteHeader(tc.tokenStatus)
						fmt.Fprint(w, hostile)
						return
					}
					if tc.tokenBody != "" {
						fmt.Fprint(w, tc.tokenBody)
						return
					}
					fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"fake-refresh","expires_in":3600}`, fakeJWT("fake-account"))
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var prompt DevicePrompt
			got, err := acquire(ctx, srv.Client(), srv.URL, func(p DevicePrompt) {
				prompt = p
				if tc.cancel {
					cancel()
				}
			})
			if prompt.UserCode != "FAKE-PUBLIC" || prompt.VerificationURL != "https://auth.openai.com/codex/device" {
				t.Errorf("public prompt: %+v", prompt)
			}
			if tc.wantSuccess {
				if err != nil {
					t.Fatal(err)
				}
				if got.AccessToken != fakeJWT("fake-account") || got.RefreshToken != "fake-refresh" || got.AccountID != "fake-account" || !got.ExpiresAt.After(time.Now().Add(59*time.Minute)) || seenCode != "fake-code" || polls.Load() != 4 {
					t.Errorf("incomplete credential or sequence: %+v polls=%d", got, polls.Load())
				}
			} else {
				if err == nil || strings.Contains(err.Error(), hostile) || strings.Contains(err.Error(), "fake-device-secret") || strings.Contains(err.Error(), "fake-verifier") || got.AccessToken != "" || got.RefreshToken != "" {
					t.Errorf("unsafe failure: credential=%+v err=%v", got, err)
				}
			}
		})
	}
}
