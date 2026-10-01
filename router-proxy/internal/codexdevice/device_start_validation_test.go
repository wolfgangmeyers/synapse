package codexdevice

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const deviceStartHostileMarker = "DEVICE_START_HOSTILE_MARKER"

// Only the external issuer is replaced. acquire retains its request, decoding,
// validation, notification and cancellation paths without opening a socket.
type deviceStartValidationTransport struct {
	body       string
	starts     int
	polls      int
	exchanges  int
	unexpected int
}

func (m *deviceStartValidationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		defer req.Body.Close()
	}
	if req.Method != http.MethodPost || req.URL.Scheme != "https" || req.URL.Host != "auth.openai.com" {
		m.unexpected++
		return nil, errors.New("unexpected device request")
	}
	switch req.URL.Path {
	case "/api/accounts/deviceauth/usercode":
		m.starts++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(m.body)),
			Request:    req,
		}, nil
	case "/api/accounts/deviceauth/token":
		m.polls++
	case "/oauth/token":
		m.exchanges++
	default:
		m.unexpected++
	}
	return nil, errors.New("unexpected device request")
}

func (m *deviceStartValidationTransport) assertStartOnly(t *testing.T) {
	t.Helper()
	if m.starts != 1 || m.polls != 0 || m.exchanges != 0 || m.unexpected != 0 {
		t.Errorf("issuer requests: start=%d poll=%d exchange=%d unexpected=%d, want 1/0/0/0", m.starts, m.polls, m.exchanges, m.unexpected)
	}
}

func TestDeviceStartMalformedAndInvalidIntervalRejectsCredential(t *testing.T) {
	prefix := `{"device_auth_id":"` + deviceStartHostileMarker + `","user_code":"PUBLIC","interval":`
	for _, tc := range []struct {
		name string
		body string
	}{
		{"malformed_json", `{"device_auth_id":"` + deviceStartHostileMarker + `"`},
		{"missing_device_id", `{"user_code":"` + deviceStartHostileMarker + `"}`},
		{"empty_device_id", `{"device_auth_id":"","user_code":"` + deviceStartHostileMarker + `"}`},
		{"missing_user_code", `{"device_auth_id":"` + deviceStartHostileMarker + `"}`},
		{"empty_user_code", `{"device_auth_id":"` + deviceStartHostileMarker + `","user_code":""}`},
		{"oversized_device_id", `{"device_auth_id":"` + strings.Repeat("D", 4097) + `","user_code":"` + deviceStartHostileMarker + `"}`},
		{"oversized_user_code", `{"device_auth_id":"` + deviceStartHostileMarker + `","user_code":"` + strings.Repeat("U", 257) + `"}`},
		{"wrong_interval_type", prefix + `true}`},
		{"negative_interval", prefix + `-1}`},
		{"interval_above_900", prefix + `900.001}`},
		{"interval_float_overflow", prefix + `1e309}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &deviceStartValidationTransport{body: tc.body}
			client := &http.Client{Transport: transport}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var prompts []DevicePrompt
			got, err := acquire(ctx, client, issuer, func(prompt DevicePrompt) {
				prompts = append(prompts, prompt)
				// A regression must fail promptly instead of entering a poll wait.
				cancel()
			})
			transport.assertStartOnly(t)
			if len(prompts) != 0 {
				t.Errorf("prompt callbacks = %d, want 0", len(prompts))
			}
			if got != (store.CodexCredential{}) {
				t.Error("invalid start returned a nonempty credential")
			}
			if err == nil {
				t.Fatal("invalid start returned no error")
			}
			if strings.Contains(err.Error(), deviceStartHostileMarker) {
				t.Error("start error disclosed a private response marker")
			}
			if err.Error() != "device start invalid" {
				t.Error("start error did not match the fixed device start invalid rejection")
			}
		})
	}
}

func TestDeviceStartAcceptedIntervalsNotifyBeforeCancellation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval string
		want     time.Duration
	}{
		{"missing_interval", "", 5 * time.Second},
		{"zero_interval", `,"interval":0`, time.Second},
		{"fractional_interval", `,"interval":0.5`, time.Second},
		{"maximum_interval", `,"interval":900`, 900 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &deviceStartValidationTransport{
				body: `{"device_auth_id":"` + deviceStartHostileMarker + `","user_code":"PUBLIC"` + tc.interval + `}`,
			}
			client := &http.Client{Transport: transport}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var prompts []DevicePrompt
			got, err := acquire(ctx, client, issuer, func(prompt DevicePrompt) {
				prompts = append(prompts, prompt)
				cancel()
			})
			transport.assertStartOnly(t)
			if len(prompts) != 1 {
				t.Fatalf("prompt callbacks = %d, want 1", len(prompts))
			}
			prompt := prompts[0]
			if prompt.Interval != tc.want || prompt.UserCode != "PUBLIC" || prompt.VerificationURL != verificationURL || prompt.ExpiresIn != 900*time.Second {
				t.Error("accepted start returned an unexpected public prompt or interval")
			}
			if got != (store.CodexCredential{}) {
				t.Error("cancelled acquisition returned a nonempty credential")
			}
			if err == nil {
				t.Fatal("cancelled acquisition returned no error")
			}
			if strings.Contains(err.Error(), deviceStartHostileMarker) {
				t.Error("cancellation error disclosed a private response marker")
			}
			if err.Error() != "device flow cancelled" {
				t.Error("cancellation error did not match the fixed device flow cancelled result")
			}
		})
	}
}
