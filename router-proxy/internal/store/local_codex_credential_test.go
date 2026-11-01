package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/stretchr/testify/require"
)

func TestCodexCredentialAncestorPermissions(t *testing.T) {
	root := testfixture.PrivateDir(t)

	tests := []struct {
		name         string
		ancestorMode os.FileMode
		destMode     os.FileMode
		wantErr      bool
		errSubstring string
	}{
		{
			name:         "ancestor 0755 accepted",
			ancestorMode: 0o755,
			destMode:     0o700,
			wantErr:      false,
		},
		{
			name:         "ancestor 0750 accepted",
			ancestorMode: 0o750,
			destMode:     0o700,
			wantErr:      false,
		},
		{
			name:         "ancestor 0700 accepted",
			ancestorMode: 0o700,
			destMode:     0o700,
			wantErr:      false,
		},
		{
			name:         "ancestor 0770 group writable rejected",
			ancestorMode: 0o770,
			destMode:     0o700,
			wantErr:      true,
			errSubstring: "credential ancestor is writable",
		},
		{
			name:         "ancestor 0777 world writable rejected",
			ancestorMode: 0o777,
			destMode:     0o700,
			wantErr:      true,
			errSubstring: "credential ancestor is writable",
		},
		{
			name:         "destination 0755 accepted",
			ancestorMode: 0o755,
			destMode:     0o755,
			wantErr:      false,
		},
		{name: "destination 0750 accepted", ancestorMode: 0o755, destMode: 0o750},
		{name: "destination 0775 accepted", ancestorMode: 0o755, destMode: 0o775},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caseDir := filepath.Join(root, t.Name())
			require.NoError(t, os.MkdirAll(caseDir, 0o700))

			ancestorDir := filepath.Join(caseDir, "ancestor")
			require.NoError(t, os.Mkdir(ancestorDir, tc.ancestorMode))
			require.NoError(t, os.Chmod(ancestorDir, tc.ancestorMode))

			destDir := filepath.Join(ancestorDir, ".mecha-developer")
			require.NoError(t, os.Mkdir(destDir, tc.destMode))
			require.NoError(t, os.Chmod(destDir, tc.destMode))

			credPath := filepath.Join(destDir, "codex.credential")
			s, err := store.OpenLocalCodexCredentialStore(credPath)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errSubstring != "" {
					require.Contains(t, err.Error(), tc.errSubstring)
				}
				return
			}

			require.NoError(t, err)
			ctx := context.Background()
			cred := store.CodexCredential{
				AccessToken:  "test-access-token",
				RefreshToken: "test-refresh-token",
			}
			require.NoError(t, s.Save(ctx, cred))

			loaded, err := s.Load(ctx)
			require.NoError(t, err)
			require.Equal(t, cred, loaded)
		})
	}
}
