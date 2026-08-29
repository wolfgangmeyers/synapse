package attachments

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func pngFixture(width, height uint32) []byte {
	data := make([]byte, 24)
	copy(data, []byte("\x89PNG\r\n\x1a\n"))
	copy(data[12:], []byte("IHDR"))
	data[16], data[17], data[18], data[19] = byte(width>>24), byte(width>>16), byte(width>>8), byte(width)
	data[20], data[21], data[22], data[23] = byte(height>>24), byte(height>>16), byte(height>>8), byte(height)
	return data
}

func TestAttachmentLifecycleUsesPrivateDirectoryAndAtomicFinalize(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	store, err := New(Options{Directory: filepath.Join(t.TempDir(), "images"), Clock: clock})
	require.NoError(t, err)
	data := pngFixture(2, 3)
	pending, err := store.WriteTemp("message-1", 1, Input{MIMEType: "image/png", Name: "screen shot.png", Bytes: data})
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(store.directory, pending.ID))
	require.NoError(t, err, "the published file exists before the caller writes its database row")
	require.NoError(t, store.Finalize(pending), "Finalize remains a safe compatibility check after publication")
	require.NoError(t, store.Discard(pending), "the caller can compensate for a failed database enqueue")
	pending, err = store.WriteTemp("message-1", 1, Input{MIMEType: "image/png", Name: "screen shot.png", Bytes: data})
	require.NoError(t, err)
	got, metadata, err := store.Read(pending.ID)
	require.NoError(t, err)
	require.Equal(t, data, got)
	require.Equal(t, "image/png", metadata.MIMEType)
	require.Equal(t, "", metadata.Name, "filesystem reads do not persist display names")
	require.Equal(t, 0o600, int(metadataMode(t, filepath.Join(store.directory, pending.ID))))
	require.NoError(t, store.Delete(pending.ID))
	_, _, err = store.Read(pending.ID)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestDiscardPreservesExistingReferencedAttachmentAfterIdempotentRetry(t *testing.T) {
	references := map[string]bool{}
	store, err := New(Options{Directory: filepath.Join(t.TempDir(), "images"), Referenced: func(id string) bool { return references[id] }})
	require.NoError(t, err)
	input := Input{MIMEType: "image/png", Bytes: pngFixture(1, 1)}
	original, err := store.WriteTemp("same-message", 1, input)
	require.NoError(t, err)
	references[original.ID] = true
	duplicate, err := store.WriteTemp("same-message", 1, input)
	require.NoError(t, err)
	require.True(t, duplicate.preexisting)
	require.True(t, duplicate.preexistingReferenced)
	require.NoError(t, store.Discard(duplicate))
	got, _, err := store.Read(original.ID)
	require.NoError(t, err)
	require.Equal(t, input.Bytes, got)
}

func TestDiscardPreservesPreexistingAttachmentWithoutReferenceCallback(t *testing.T) {
	store, err := New(Options{Directory: filepath.Join(t.TempDir(), "images")})
	require.NoError(t, err)
	input := Input{MIMEType: "image/png", Bytes: pngFixture(1, 1)}
	original, err := store.WriteTemp("same-message", 1, input)
	require.NoError(t, err)
	duplicate, err := store.WriteTemp("same-message", 1, input)
	require.NoError(t, err)
	require.True(t, duplicate.preexisting)
	require.NoError(t, store.Discard(duplicate))
	got, _, err := store.Read(original.ID)
	require.NoError(t, err)
	require.Equal(t, input.Bytes, got)
}

func TestAttachmentAdmissionMatchesNodeCountAndByteLimits(t *testing.T) {
	store, err := New(Options{Directory: filepath.Join(t.TempDir(), "images"), MaxBytes: 24, MaxTotalBytes: 48})
	require.NoError(t, err)
	valid := Input{MIMEType: "image/png", Bytes: pngFixture(1, 1)}
	require.NoError(t, store.ValidateBatch([]Input{valid, valid}))
	require.ErrorIs(t, store.ValidateBatch([]Input{valid, valid, valid, valid}), ErrAttachmentTooLarge)
	overLimit := append([]byte{}, pngFixture(1, 1)...)
	overLimit = append(overLimit, 0)
	require.ErrorIs(t, store.ValidateBatch([]Input{valid, Input{MIMEType: "image/png", Bytes: overLimit}}), ErrAttachmentTooLarge)
	require.ErrorIs(t, store.ValidateBatch([]Input{{MIMEType: "image/jpeg", Bytes: pngFixture(1, 1)}}), ErrInvalidImage)
}

func TestAttachmentSweepKeepsReferencedAndRemovesExpiredFiles(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	references := map[string]bool{}
	store, err := New(Options{Directory: filepath.Join(t.TempDir(), "images"), Clock: clock, Retention: time.Hour, Referenced: func(id string) bool { return references[id] }})
	require.NoError(t, err)
	old, err := store.WriteTemp("message-1", 1, Input{MIMEType: "image/png", Bytes: pngFixture(1, 1)})
	require.NoError(t, err)
	require.NoError(t, store.Finalize(old))
	kept, err := store.WriteTemp("message-2", 1, Input{MIMEType: "image/png", Bytes: pngFixture(1, 1)})
	require.NoError(t, err)
	require.NoError(t, store.Finalize(kept))
	require.NoError(t, os.Chtimes(filepath.Join(store.directory, old.ID), start.Add(-2*time.Hour), start.Add(-2*time.Hour)))
	require.NoError(t, os.Chtimes(filepath.Join(store.directory, kept.ID), start.Add(-2*time.Hour), start.Add(-2*time.Hour)))
	references[kept.ID] = true
	removed, err := store.Sweep(nil)
	require.NoError(t, err)
	require.Equal(t, []string{old.ID}, removed)
	_, _, err = store.Read(old.ID)
	require.ErrorIs(t, err, os.ErrNotExist)
	got, _, err := store.Read(kept.ID)
	require.NoError(t, err, "referenced stale files remain readable")
	require.Equal(t, pngFixture(1, 1), got)
}

func TestAttachmentDirectoryRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	require.NoError(t, os.Mkdir(target, 0o700))
	link := filepath.Join(parent, "images")
	require.NoError(t, os.Symlink(target, link))
	_, err := New(Options{Directory: link})
	require.ErrorIs(t, err, ErrInvalidDirectory)
	require.False(t, errors.Is(err, os.ErrNotExist))
}

func metadataMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Mode().Perm()
}
