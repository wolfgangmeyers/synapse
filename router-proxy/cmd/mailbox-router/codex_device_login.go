package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/codexdevice"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const codexDeviceVerificationURL = "https://auth.openai.com/codex/device"

type deviceAcquireFunc func(context.Context, func(codexdevice.DevicePrompt)) (store.CodexCredential, error)

// runCodexDeviceLogin is deliberately independent of router configuration and startup.
// Only a clean absolute destination path is accepted; the store enforces its private tree.
func runCodexDeviceLogin(ctx context.Context, args []string, stdout, _ io.Writer, acquire deviceAcquireFunc) error {
	if len(args) != 1 {
		return errors.New("usage: mailbox-router codex-device-login <absolute-private-credential-path>")
	}
	credentials, err := store.OpenLocalCodexCredentialStore(args[0])
	if err != nil {
		return errors.New("invalid private credential path")
	}
	if acquire == nil {
		return errors.New("device acquisition unavailable")
	}
	err = credentials.Update(ctx, func(store.CodexCredential) (store.CodexCredential, error) {
		prompted, invalid := false, false
		credential, issueErr := acquire(ctx, func(prompt codexdevice.DevicePrompt) {
			if prompted || !codexdevice.ValidUserCode(prompt.UserCode) {
				invalid = true
				return
			}
			prompted = true
			if _, e := fmt.Fprintf(stdout, "%s\n%s\n", codexDeviceVerificationURL, prompt.UserCode); e != nil {
				invalid = true
			}
		})
		if issueErr != nil || invalid || !prompted || ctx.Err() != nil || credential.AccountID == "" || credential.ExpiresAt.IsZero() || !credential.ExpiresAt.After(time.Now()) {
			return store.CodexCredential{}, errors.New("Codex device login failed")
		}
		return credential, nil
	})
	if err != nil {
		return errors.New("Codex device login failed")
	}
	return nil
}

func productionCodexDeviceLogin(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	client := &http.Client{Timeout: 30 * time.Second}
	return runCodexDeviceLogin(ctx, args, stdout, stderr, func(ctx context.Context, notify func(codexdevice.DevicePrompt)) (store.CodexCredential, error) {
		return codexdevice.Acquire(ctx, client, notify)
	})
}
