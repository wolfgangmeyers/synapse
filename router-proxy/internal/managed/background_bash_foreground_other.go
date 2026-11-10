//go:build !linux

package managed

import (
	"context"
	"errors"

	"github.com/sky-valley/pi/agent"
)

func (m *Manager) executePromotableBash(context.Context, embeddedOwner, string, embeddedBashCommand) (agent.AgentToolResult, error) {
	return agent.AgentToolResult{}, errors.New("foreground promotion unsupported on this platform")
}
