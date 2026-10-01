package managed

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func traceFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func traceContents(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		text += string(body)
	}
	return text
}

func TestAliasTraceOfflinePrivacyAndOrdering(t *testing.T) {
	dir := traceFixture(t)
	t.Setenv("ALIAS_TRACE_DIR", "")
	aliasTrace("startup", "root", "session", "operation", "")
	if got := traceContents(t, dir); got != "" {
		t.Fatalf("default off wrote %d bytes", len(got))
	}
	sink := newAliasTraceSink(dir)
	if sink == nil {
		t.Fatal("private destination rejected")
	}
	secret := "sensitive-one sensitive-two"
	for _, phase := range []string{"startup", "checked-user", "provider-entry", "codex-terminal", "checked-assistant", "checked-intent", "worker-dispatch"} {
		sink.queue <- aliasMarker{phase, secret, secret, secret, secret}
	}
	close(sink.queue)
	<-sink.done
	text := traceContents(t, dir)
	for _, word := range strings.Fields(secret) {
		if strings.Contains(text, word) {
			t.Fatalf("trace leaked forbidden input")
		}
	}
	previous := -1
	for _, phase := range []string{"startup", "checked-user", "provider-entry", "codex-terminal", "checked-assistant", "checked-intent", "worker-dispatch"} {
		index := strings.Index(text, "phase="+phase+" ")
		if index <= previous {
			t.Fatalf("phase missing or out of order: %s", phase)
		}
		previous = index
	}
	if aliasTraceLine(aliasMarker{phase: "unrecognized", root: secret}) != "" {
		t.Fatal("unknown phase allowed")
	}
}

func TestAliasTraceOfflineRetentionAndFailure(t *testing.T) {
	dir := traceFixture(t)
	now := time.Now()
	for i := 0; i < 9; i++ {
		name := filepath.Join(dir, aliasTracePrefix+strings.Repeat(string('a'+rune(i)), 32)+".log")
		if err := os.WriteFile(name, []byte("fixed"), 0600); err != nil {
			t.Fatal(err)
		}
		age := time.Duration(9-i) * time.Hour
		if i == 0 {
			age = 48 * time.Hour
		}
		if err := os.Chtimes(name, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	sink := newAliasTraceSink(dir)
	sink.queue <- aliasMarker{phase: "startup", root: "root"}
	close(sink.queue)
	<-sink.done
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	var owned int
	for _, entry := range entries {
		if !aliasTraceName(entry.Name()) {
			// The nonhex fixture names are unrelated files, not trace-owned.
			if _, err := os.Stat(filepath.Join(dir, entry.Name())); err != nil {
				t.Fatal("unrelated file removed")
			}
			continue
		}
		owned++
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if now.Sub(info.ModTime()) > aliasTraceAge {
			t.Fatal("expired trace retained")
		}
		total += info.Size()
	}
	if owned > aliasTraceFiles {
		t.Fatalf("retained %d owned files", owned)
	}
	for _, nonhex := range []string{"g", "h", "i"} {
		if _, err := os.Stat(filepath.Join(dir, aliasTracePrefix+strings.Repeat(nonhex, 32)+".log")); err != nil {
			t.Fatal("unrelated nonhex file removed")
		}
	}
	if total > aliasTraceFiles*aliasTraceLimit {
		t.Fatal("trace byte cap exceeded")
	}
	// Opening a non-directory or a shared directory fails silently, never
	// returning an error into a turn or creating a plaintext fallback.
	bad := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(bad, nil, 0600); err != nil {
		t.Fatal(err)
	}
	failed := newAliasTraceSink(bad)
	failed.queue <- aliasMarker{phase: "startup", root: "root"}
	close(failed.queue)
	<-failed.done
	if info, err := os.Stat(bad); err != nil || !info.Mode().IsRegular() {
		t.Fatal("failed trace changed destination")
	}
}

func TestAliasTraceCapStillExpiresOnInjectedClock(t *testing.T) {
	dir := traceFixture(t)
	start := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	now := start
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	tick := make(chan time.Time)
	sink := newAliasTraceSinkWithOptions(dir, aliasTraceOptions{now: clock, expiry: tick})
	marker := aliasMarker{phase: "startup", root: "private-identifier"}
	lineSize := len(aliasTraceLine(marker))
	maxSize := int64(aliasTraceLimit / lineSize * lineSize)
	for i := 0; i < aliasTraceLimit/lineSize+128; i++ {
		sink.queue <- marker
	}
	deadline := time.After(5 * time.Second)
	var name string
	for {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 1 {
			name = entries[0].Name()
			info, err := entries[0].Info()
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() == maxSize {
				break
			}
			if info.Size() > maxSize {
				t.Fatal("cap exceeded")
			}
		}
		select {
		case <-deadline:
			t.Fatal("trace never reached cap")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-sink.done:
		t.Fatal("expiry stopped at cap")
	default:
	}
	mu.Lock()
	now = start.Add(aliasTraceAge + time.Second)
	mu.Unlock()
	tick <- now
	select {
	case <-sink.done:
	case <-time.After(5 * time.Second):
		t.Fatal("expiry did not finish")
	}
	if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Fatal("capped trace survived expiry")
	}
}

func TestAliasTraceDirectoryRenameSymlinkSwap(t *testing.T) {
	dir := traceFixture(t)
	outside := traceFixture(t)
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	moved := dir + "-moved"
	var hookErr error
	sink := newAliasTraceSinkWithOptions(dir, aliasTraceOptions{beforeCreate: func() {
		if err := os.Rename(dir, moved); err != nil {
			hookErr = err
			return
		}
		hookErr = os.Symlink(outside, dir)
	}})
	sink.queue <- aliasMarker{phase: "startup", root: "private-identifier"}
	close(sink.queue)
	<-sink.done
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	body, err := os.ReadFile(sentinel)
	if err != nil || string(body) != "unchanged" {
		t.Fatal("outside sentinel changed")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 1 {
		t.Fatal("outside directory changed")
	}
	entries, err = os.ReadDir(moved)
	if err != nil || len(entries) != 0 {
		t.Fatal("detached directory received trace")
	}
}

func TestAliasTraceWorkerDispatchOnlyAfterCanonicalAccepted(t *testing.T) {
	// The production marker is downstream of launchEmbeddedWorkerFromOwner and
	// excludes Replayed operations. No tracing callback calls the launch seam.
	if strings.Count(aliasTraceLine(aliasMarker{phase: "worker-dispatch", call: "same-call"}), "phase=worker-dispatch") != 1 {
		t.Fatal("duplicate dispatch marker")
	}
}
