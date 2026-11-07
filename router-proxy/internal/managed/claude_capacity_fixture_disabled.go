//go:build !claude_capacity_test_only

package managed

import "github.com/sky-valley/pi/agent"

func capacityFixtureConfigure(*Config)                                {}
func capacityFixtureWrap(t agent.AgentTool, _ string) agent.AgentTool { return t }
