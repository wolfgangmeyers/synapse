package managed

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func hashTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func hashTestTree(t *testing.T, path string) string {
	t.Helper()
	type entry struct {
		name string
		data []byte
	}
	var entries []entry
	require.NoError(t, filepath.WalkDir(path, func(candidate string, item os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() {
			if item.Name() == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(candidate)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(path, candidate)
		if err != nil {
			return err
		}
		entries = append(entries, entry{filepath.ToSlash(relative), data})
		return nil
	}))
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	h := sha256.New()
	for _, item := range entries {
		_, _ = h.Write([]byte(item.name))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(item.data)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestEnabledConfigValidatesAllPinnedRuntimeInputs(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	workRoot := t.TempDir()
	home := t.TempDir()
	sessionRoot := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	cursorPackage := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cursorPackage, "package.json"), []byte(`{"name":"@mecha/cursor","version":"1.0.0"}`), 0o600))
	cursor := filepath.Join(cursorPackage, "extension.mjs")
	require.NoError(t, os.WriteFile(cursor, []byte("export default function () {}\n"), 0o600))
	cursorLock := filepath.Join(cursorPackage, "package-lock.json")
	require.NoError(t, os.WriteFile(cursorLock, []byte(`{"lockfileVersion":3}`), 0o600))
	ui := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ui, "index.html"), []byte("<!doctype html>"), 0o600))
	pi := filepath.Join(t.TempDir(), "pi")
	require.NoError(t, os.WriteFile(pi, []byte("#!/bin/sh\nprintf '0.85.0\\n'\n"), 0o700))
	manifest := filepath.Join(root, "router", "contract", "managed-tools-v1.json")
	mecha := filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts")
	subagents := filepath.Join(os.Getenv("HOME"), "code", "pi-packages", "packages", "pi-subagents", "src", "index.ts")
	helper := filepath.Join(root, "auth-helper", "index.mjs")
	helperLock := filepath.Join(root, "auth-helper", "package-lock.json")
	if _, err := os.Stat(subagents); err != nil {
		t.Skip("checked pi-subagents workspace is required")
	}
	expected := ExpectedIdentities{PiVersion: "0.85.0", PiSHA256: hashTestFile(t, pi), ManifestSHA256: hashTestFile(t, manifest), MechaRPCHash: hashTestFile(t, mecha), PiSubagentsEntryHash: hashTestFile(t, subagents), PiSubagentsTreeHash: hashTestTree(t, filepath.Dir(filepath.Dir(subagents))), CursorExtensionHash: hashTestFile(t, cursor), CursorPackageHash: hashTestFile(t, filepath.Join(cursorPackage, "package.json")), CursorResolvedIdentity: "@mecha/cursor@1.0.0", CursorLockfileHash: hashTestFile(t, cursorLock), CursorTreeHash: hashTestTree(t, cursorPackage), HelperHash: hashTestFile(t, helper), HelperLockfileHash: hashTestFile(t, helperLock), HelperTreeHash: hashTestTree(t, filepath.Dir(helper)), UIDistributionTreeHash: hashTestTree(t, ui)}
	config := Config{Enabled: true, PiExecutable: pi, ManagedHome: home, SessionRoot: sessionRoot, ManifestPath: manifest, MechaRPCExtension: mecha, PiSubagentsExtension: subagents, CursorExtension: cursor, CursorPackage: cursorPackage, CursorLockfile: cursorLock, HelperPath: helper, HelperLockfile: helperLock, UIDistribution: ui, AllowedWorkingDirectoryRoots: []string{workRoot}, CanonicalQuietRecipientID: "supervisor", Expected: expected}
	require.NoError(t, config.Validate())
	config.Expected.MechaRPCHash = "tampered"
	require.Error(t, config.Validate())
}

func TestHelperSourceTreeVerifierRejectsImportedModuleTampering(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"index.mjs", "providers.mjs", "protocol.mjs"} {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(name+"\n"), 0o600))
	}
	expected := hashTestTree(t, directory)
	require.NoError(t, verifyTreeHash(directory, expected))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "providers.mjs"), []byte("tampered\n"), 0o600))
	require.Error(t, verifyTreeHash(directory, expected))
}

func TestFixedInputTreeVerifierRejectsTamperingAndSymlinks(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "file")
	require.NoError(t, os.WriteFile(file, []byte("one"), 0o600))
	expected := hashTestTree(t, directory)
	require.NoError(t, verifyTreeHash(directory, expected))
	require.Error(t, verifyTreeHash(directory, "not-the-tree"))
	link := filepath.Join(directory, "link")
	require.NoError(t, os.Symlink(file, link))
	require.Error(t, verifyTreeHash(directory, expected))
}
