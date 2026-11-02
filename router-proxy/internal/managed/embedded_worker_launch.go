package managed

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func embeddedWorkerRequestID(binding store.EmbeddedBinding, callID string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("embedded-worker-launch\x00%s\x00%s\x00%d\x00%s\x00%s", binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, callID)))
	digest[6] = (digest[6] & 15) | 64
	digest[8] = (digest[8] & 63) | 128
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16])
}

func (m *Manager) authorizeEmbeddedWorkerLaunch(ctx context.Context, owner embeddedOwner) error {
	if err := m.store.RenewEmbeddedOwner(ctx, owner.binding, owner.identity, embeddedOwnerTTL); err != nil {
		return err
	}
	role, err := m.store.EmbeddedSessionRole(ctx, owner.binding)
	if err != nil {
		return err
	}
	if role != "root" && role != "worker" {
		return errors.New("embedded worker launch requires root or worker capability")
	}
	return nil
}
