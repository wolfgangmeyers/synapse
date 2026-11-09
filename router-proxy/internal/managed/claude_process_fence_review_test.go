package managed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeExternalAdmissionShutdownBarrier(t *testing.T) {
	var pool claudeCodeProcessPool
	done, err := pool.admitExternal()
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- pool.stopAll() }()
	for {
		pool.mu.Lock()
		shutting := pool.closed
		pool.mu.Unlock()
		if shutting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := pool.admitExternal(); err == nil {
		t.Fatal("admission accepted after shutdown")
	}
	select {
	case <-closed:
		t.Fatal("shutdown skipped admitted operation")
	default:
	}
	done()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
}

func TestClaudeExternalAdmissionRejectedWhenFenced(t *testing.T) {
	var pool claudeCodeProcessPool
	pool.fence()
	if _, err := pool.admitExternal(); err == nil {
		t.Fatal("fenced pool admitted work")
	}
}

func TestClaudeOrphanReplacementFencesAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record")
	id := ProcessIdentity{PID: 123, StartMarker: "start", Incarnation: "original"}
	replacement := id
	replacement.Incarnation = "replacement"
	data, err := json.Marshal(claudeCodeOrphanRecord{Version: 1, Boot: "boot", Identity: replacement})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var pool claudeCodeProcessPool
	p := &ClaudeCodeProcess{orphanPath: path, orphanIdentity: id, onUnconfirmedStop: pool.fenceStartup}
	p.forgetClaudeOrphanConfirmed()
	if _, err := pool.admitExternal(); err == nil {
		t.Fatal("stale orphan did not fence")
	}
}
