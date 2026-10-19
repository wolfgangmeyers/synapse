package managed

import (
	"context"
	"os"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// OpenTestStoreHelper exports openTestStore for black-box tests in package managed_test.
func OpenTestStoreHelper(t testing.TB, ctx context.Context, path string, options store.Options) (*store.Store, error) {
	return openTestStore(t, ctx, path, options)
}

func openTestStore(t testing.TB, ctx context.Context, path string, options store.Options) (*store.Store, error) {
	if t != nil {
		t.Helper()
	}
	if options.Migrations == nil {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := store.CloneTemplateDB(t, path); err == nil {
				options.Migrations = []store.Migration{}
			}
		} else if err == nil {
			options.Migrations = []store.Migration{}
		}
	}
	return store.Open(ctx, path, options)
}
