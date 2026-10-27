package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeriveKeyFromEnvVar(t *testing.T) {
	// 1. Raw 32 bytes
	rawKey := "12345678901234567890123456789012"
	t.Setenv("MECHA_SECRET_KEY", rawKey)
	key, err := DeriveKey("")
	require.NoError(t, err)
	require.Equal(t, []byte(rawKey), key)

	// 2. Base64 encoded 32 bytes
	randomKey := make([]byte, 32)
	_, err = rand.Read(randomKey)
	require.NoError(t, err)

	b64Key := base64.StdEncoding.EncodeToString(randomKey)
	t.Setenv("MECHA_SECRET_KEY", b64Key)
	key, err = DeriveKey("")
	require.NoError(t, err)
	require.Equal(t, randomKey, key)

	// 3. Hex encoded 32 bytes
	hexKey := hex.EncodeToString(randomKey)
	t.Setenv("MECHA_SECRET_KEY", hexKey)
	key, err = DeriveKey("")
	require.NoError(t, err)
	require.Equal(t, randomKey, key)

	// 4. Invalid length should fall through
	t.Setenv("MECHA_SECRET_KEY", "too-short")
	fallbackKey, err := DeriveKey("")
	require.NoError(t, err)
	require.Len(t, fallbackKey, 32)
	require.NotEqual(t, []byte("too-short"), fallbackKey)
}

func TestDeriveKeyFromFile(t *testing.T) {
	// Clear env var
	t.Setenv("MECHA_SECRET_KEY", "")

	tempDir := t.TempDir()
	require.NoError(t, os.Chmod(tempDir, 0o755))
	stateDir := filepath.Join(tempDir, "state")

	// File doesn't exist yet -> DeriveKey creates it with 0600
	key1, err := DeriveKey(stateDir)
	require.NoError(t, err)
	require.Len(t, key1, 32)

	keyFile := filepath.Join(stateDir, "secret.key")
	fi, err := os.Stat(keyFile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	dirInfo, err := os.Stat(stateDir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	// Calling DeriveKey again loads the same key
	key2, err := DeriveKey(stateDir)
	require.NoError(t, err)
	require.Equal(t, key1, key2)
}

func TestDeriveKeyHKDFFallback(t *testing.T) {
	t.Setenv("MECHA_SECRET_KEY", "")

	key1, err := DeriveKey("")
	require.NoError(t, err)
	require.Len(t, key1, 32)

	// Derivation from empty or fixed state dir without file should be deterministic
	key2, err := DeriveKey("")
	require.NoError(t, err)
	require.Equal(t, key1, key2)
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	plaintext := []byte("hello oauth secret token data 12345!")

	ciphertext, err := Encrypt(plaintext, key)
	require.NoError(t, err)
	require.NotEmpty(t, ciphertext)

	decrypted, err := Decrypt(ciphertext, key)
	require.NoError(t, err)
	require.Equal(t, plaintext, decrypted)
}

func TestEncryptDecryptErrors(t *testing.T) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)

	// Invalid key length
	_, err = Encrypt([]byte("data"), key[:16])
	require.Error(t, err)

	_, err = Decrypt("some-b64", key[:16])
	require.Error(t, err)

	// Corrupt ciphertext
	ciphertext, err := Encrypt([]byte("secret message"), key)
	require.NoError(t, err)

	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	require.NoError(t, err)
	raw[len(raw)-1] ^= 0xff // flip bit in authentication tag
	tamperedB64 := base64.StdEncoding.EncodeToString(raw)

	_, err = Decrypt(tamperedB64, key)
	require.Error(t, err)

	// Decrypt with wrong key
	wrongKey := make([]byte, 32)
	copy(wrongKey, key)
	wrongKey[0] ^= 0x01
	_, err = Decrypt(ciphertext, wrongKey)
	require.Error(t, err)

	// Truncated ciphertext (shorter than nonce)
	tooShort := base64.StdEncoding.EncodeToString([]byte("short"))
	_, err = Decrypt(tooShort, key)
	require.Error(t, err)

	// Invalid base64
	_, err = Decrypt("not-valid-base64!!!", key)
	require.Error(t, err)
}
