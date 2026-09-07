package main

import (
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

func managedProcessFactory() managed.ProcessFactory { return managed.DefaultProcessFactory() }
func managedRPCFactory() managed.RPCFactory         { return managed.DefaultRPCFactory() }

func validateManagedStartup(config managed.Config) error {
	return managed.ValidateManagedLaunchConfig(config)
}

func parseManagedRoots(value string) []string {
	var roots []string
	for _, candidate := range strings.Split(value, ",") {
		if strings.TrimSpace(candidate) != "" {
			roots = append(roots, strings.TrimSpace(candidate))
		}
	}
	return roots
}
