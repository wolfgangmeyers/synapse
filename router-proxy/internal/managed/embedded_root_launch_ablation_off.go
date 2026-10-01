//go:build !embedded_root_launch_ablation

package managed

// Ordinary builds retain the root's worker-launch declaration.
const embeddedRootLaunchAblation = false
