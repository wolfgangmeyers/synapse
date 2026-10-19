package managed_test

import (
	"context"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func openTestStore(t testing.TB, ctx context.Context, path string, options store.Options) (*store.Store, error) {
	if t != nil {
		t.Helper()
	}
	return managed.OpenTestStoreHelper(t, ctx, path, options)
}
