package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

// helperCatalog is the only production model boundary used by the first
// slice. The helper is a one-shot process and its response contains no auth data.
type helperCatalog struct {
	Path     string
	AuthPath string
}

func (h helperCatalog) Catalog(ctx context.Context) (managed.Catalog, error) {
	if h.Path == "" {
		return managed.Catalog{}, errors.New("model helper path is required")
	}
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return managed.Catalog{}, errors.New("node runtime is unavailable")
	}
	command := exec.CommandContext(ctx, nodePath, h.Path)
	authPath := h.AuthPath
	home := ""
	if authPath != "" {
		home = filepath.Dir(filepath.Dir(filepath.Dir(authPath)))
	}
	command.Env = []string{
		"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + home,
		"PI_CODING_AGENT_DIR=" + filepath.Join(home, ".pi", "agent"),
		"MECHA_MANAGED_AUTH_PATH=" + authPath,
		"PI_OFFLINE=1",
		"LANG=C", "LC_ALL=C",
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return managed.Catalog{}, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return managed.Catalog{}, err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return managed.Catalog{}, err
	}
	if _, err := fmt.Fprintln(stdin, `{"operation":"models"}`); err != nil {
		return managed.Catalog{}, err
	}
	_ = stdin.Close()
	var line string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	if scanner.Scan() {
		line = scanner.Text()
	}
	if scanner.Scan() || scanner.Err() != nil {
		_ = command.Wait()
		return managed.Catalog{}, errors.New("model helper response is invalid")
	}
	if err := command.Wait(); err != nil {
		return managed.Catalog{}, errors.New("model helper failed")
	}
	var response struct {
		Models []managed.Model `json:"models"`
		Error  string          `json:"error"`
	}
	if json.Unmarshal([]byte(line), &response) != nil || response.Error != "" {
		return managed.Catalog{}, errors.New("model helper response is invalid")
	}
	return managed.Catalog{Models: response.Models}, nil
}

func managedProcessFactory() managed.ProcessFactory { return managed.DefaultProcessFactory() }
func managedRPCFactory() managed.RPCFactory         { return managed.DefaultRPCFactory() }

func parseManagedRoots(value string) []string {
	var roots []string
	for _, candidate := range strings.Split(value, ",") {
		if strings.TrimSpace(candidate) != "" {
			roots = append(roots, strings.TrimSpace(candidate))
		}
	}
	return roots
}

func managedExpectedFromEnvironment() managed.ExpectedIdentities {
	return managed.ExpectedIdentities{
		PiVersion:              os.Getenv("MECHA_MANAGED_PI_VERSION"),
		PiSHA256:               os.Getenv("MECHA_MANAGED_PI_SHA256"),
		ManifestSHA256:         os.Getenv("MECHA_MANAGED_MANIFEST_SHA256"),
		MechaRPCHash:           os.Getenv("MECHA_MANAGED_MECHA_RPC_SHA256"),
		PiSubagentsEntryHash:   os.Getenv("MECHA_MANAGED_PI_SUBAGENTS_ENTRY_SHA256"),
		PiSubagentsTreeHash:    os.Getenv("MECHA_MANAGED_PI_SUBAGENTS_TREE_SHA256"),
		CursorExtensionHash:    os.Getenv("MECHA_MANAGED_CURSOR_EXTENSION_SHA256"),
		CursorPackageHash:      os.Getenv("MECHA_MANAGED_CURSOR_PACKAGE_SHA256"),
		CursorResolvedIdentity: os.Getenv("MECHA_MANAGED_CURSOR_IDENTITY"),
		CursorLockfileHash:     os.Getenv("MECHA_MANAGED_CURSOR_LOCK_SHA256"),
		CursorTreeHash:         os.Getenv("MECHA_MANAGED_CURSOR_TREE_SHA256"),
		HelperHash:             os.Getenv("MECHA_MANAGED_HELPER_SHA256"),
		HelperLockfileHash:     os.Getenv("MECHA_MANAGED_HELPER_LOCK_SHA256"),
		HelperTreeHash:         os.Getenv("MECHA_MANAGED_HELPER_TREE_SHA256"),
		UIDistributionTreeHash: os.Getenv("MECHA_MANAGED_UI_TREE_SHA256"),
	}
}
