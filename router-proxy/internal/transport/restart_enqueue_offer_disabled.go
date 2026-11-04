//go:build !embedded_restart_test_only

package transport

import "github.com/reflexivity/mecha-developer/router-proxy/internal/managed"

func offerEnqueuedDirect(m *managed.Manager, agentID, messageID, site string) { m.OfferDirect(agentID) }
