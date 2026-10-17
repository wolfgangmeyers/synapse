package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const (
	KeySize   = 32
	NonceSize = 12
)

// DeriveKey derives or loads a stable 32-byte secret key for AES-256-GCM.
// Priority:
// 1. MECHA_SECRET_KEY environment variable (base64, hex, or raw 32 bytes).
// 2. <stateDir>/secret.key file (creating with 0600 permissions and validating ancestors).
// 3. HKDF-SHA256 derivation fallback from platform machine ID and stateDir.
func DeriveKey(stateDir string) ([]byte, error) {
	// 1. Check MECHA_SECRET_KEY env var
	if envKey := os.Getenv("MECHA_SECRET_KEY"); envKey != "" {
		key, err := parseKeyString(envKey)
		if err == nil && len(key) == KeySize {
			return key, nil
		}
	}

	// 2. Check <stateDir>/secret.key if stateDir is provided
	if stateDir != "" {
		key, err := loadOrCreateSecretFile(stateDir)
		if err == nil && len(key) == KeySize {
			return key, nil
		}
	}

	// 3. Fallback: HKDF-SHA256 derivation from platform machine identity
	return deriveHKDFKey(stateDir)
}

func parseKeyString(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	// Raw 32 bytes
	if len(s) == KeySize {
		return []byte(s), nil
	}
	// Try base64 standard
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == KeySize {
		return b, nil
	}
	// Try base64 URL
	if b, err := base64.URLEncoding.DecodeString(s); err == nil && len(b) == KeySize {
		return b, nil
	}
	// Try hex
	if b, err := hex.DecodeString(s); err == nil && len(b) == KeySize {
		return b, nil
	}
	return nil, errors.New("invalid key string: must decode to 32 bytes")
}

// loadOrCreateSecretFile securely reads or creates <stateDir>/secret.key
func loadOrCreateSecretFile(stateDir string) ([]byte, error) {
	absStateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve state dir path: %w", err)
	}

	// Verify ancestor directory safety
	if err := validateDirectoryHierarchy(absStateDir); err != nil {
		return nil, fmt.Errorf("validate directory permissions: %w", err)
	}

	keyPath := filepath.Join(absStateDir, "secret.key")

	// Check if secret file exists
	fi, err := os.Lstat(keyPath)
	if err == nil {
		// File exists - validate permissions and read
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("secret file must not be a symlink")
		}
		if !fi.Mode().IsRegular() {
			return nil, errors.New("secret file must be a regular file")
		}
		if fi.Mode().Perm() != 0o600 {
			return nil, fmt.Errorf("secret file has unsafe mode: %04o (expected 0600)", fi.Mode().Perm())
		}
		// Check ownership
		var stat syscall.Stat_t
		if err := syscall.Stat(keyPath, &stat); err != nil {
			return nil, fmt.Errorf("stat secret file: %w", err)
		}
		if stat.Uid != uint32(os.Geteuid()) && os.Geteuid() != 0 {
			return nil, errors.New("secret file must be owned by current user")
		}

		raw, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("read secret file: %w", err)
		}
		key, err := parseKeyString(string(raw))
		if err == nil && len(key) == KeySize {
			return key, nil
		}
		if len(raw) == KeySize {
			return raw, nil
		}
		return nil, errors.New("secret file contains invalid key length")
	}

	if !os.IsNotExist(err) {
		return nil, err
	}

	// Secret file does not exist: ensure leaf directory exists with 0700 permissions
	if err := os.MkdirAll(absStateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	dirInfo, err := os.Stat(absStateDir)
	if err != nil {
		return nil, err
	}
	if dirInfo.Mode().Perm() != 0o700 {
		if err := os.Chmod(absStateDir, 0o700); err != nil {
			return nil, fmt.Errorf("chmod state dir to 0700: %w", err)
		}
	}

	// Generate 32 cryptographically random bytes
	newKey := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, newKey); err != nil {
		return nil, fmt.Errorf("generate random key: %w", err)
	}

	// Write key to file with 0600 mode atomically
	encodedKey := base64.StdEncoding.EncodeToString(newKey) + "\n"
	tmpPath := keyPath + fmt.Sprintf(".tmp.%d", os.Getpid())
	if err := os.WriteFile(tmpPath, []byte(encodedKey), 0o600); err != nil {
		return nil, fmt.Errorf("write secret file temp: %w", err)
	}
	if err := os.Rename(tmpPath, keyPath); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("atomic rename secret file: %w", err)
	}

	return newKey, nil
}

// validateDirectoryHierarchy checks ancestor permissions per single-user developer policy:
// - Ancestors owned by user or root.
// - Not group- or world-writable (mode&0022 == 0), except root-owned sticky dirs (1777).
// - Private leaf directory must be owner-only (0700).
func validateDirectoryHierarchy(dirPath string) error {
	cleanPath := filepath.Clean(dirPath)
	parts := strings.Split(cleanPath, string(filepath.Separator))
	current := string(filepath.Separator)

	currentUID := uint32(os.Geteuid())

	for i, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				// Directories that do not exist yet are fine; will be created with 0700
				break
			}
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			// Resolve symlink target
			realPath, err := filepath.EvalSymlinks(current)
			if err != nil {
				return err
			}
			info, err = os.Stat(realPath)
			if err != nil {
				return err
			}
		}

		var stat syscall.Stat_t
		if err := syscall.Stat(current, &stat); err != nil {
			return err
		}

		isLeaf := (i == len(parts)-1)
		mode := info.Mode().Perm()

		if isLeaf {
			// Leaf directory if it exists: must be 0700 and owned by current user
			if stat.Uid != currentUID && currentUID != 0 {
				return fmt.Errorf("destination directory %s not owned by current user", current)
			}
			if mode != 0o700 {
				// If not 0700, attempt to chmod or fail
				if err := os.Chmod(current, 0o700); err != nil {
					return fmt.Errorf("destination directory %s must be 0700, got %04o", current, mode)
				}
			}
			continue
		}

		// Ancestor checks
		if stat.Uid == currentUID && currentUID != 0 {
			if mode&0o022 != 0 {
				return fmt.Errorf("ancestor directory %s is group or world writable (mode %04o)", current, mode)
			}
		} else if stat.Uid == 0 {
			// Root owned: sticky temp dir (01777) or not group/world writable
			if mode&0o022 != 0 && (info.Mode()&os.ModeSticky == 0) {
				return fmt.Errorf("root-owned ancestor %s is writable without sticky bit (mode %04o)", current, mode)
			}
		} else {
			// Single-user workstation: ancestors should be root or current user
			if mode&0o022 != 0 {
				return fmt.Errorf("ancestor directory %s has unsafe ownership and permissions", current)
			}
		}
	}
	return nil
}

// deriveHKDFKey derives a stable 32-byte key using HKDF-SHA256 from platform machine ID.
func deriveHKDFKey(stateDir string) ([]byte, error) {
	machineID := getMachineID()
	ikm := []byte(machineID + ":" + stateDir)
	salt := []byte("mecha-developer-mcp-v1")
	info := []byte("router-proxy-mcp-secret-key")
	return hkdfSHA256(ikm, salt, info, KeySize), nil
}

// getMachineID fetches a persistent host identifier across macOS, Linux, and Windows.
func getMachineID() string {
	// 1. Linux machine-id
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if data, err := os.ReadFile(p); err == nil {
			s := strings.TrimSpace(string(data))
			if s != "" {
				return s
			}
		}
	}

	// 2. macOS IOPlatformUUID
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
		if out, err := cmd.Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "IOPlatformUUID") {
					parts := strings.Split(line, "=")
					if len(parts) == 2 {
						return strings.Trim(strings.TrimSpace(parts[1]), "\"")
					}
				}
			}
		}
	}

	// 3. Hostname fallback
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		return hostname
	}

	return "default-mecha-instance"
}

// hkdfSHA256 implements RFC 5869 Extract-and-Expand with SHA-256.
func hkdfSHA256(ikm, salt, info []byte, length int) []byte {
	if len(salt) == 0 {
		salt = make([]byte, sha256.Size)
	}
	// Extract: PRK = HMAC-Hash(salt, IKM)
	h := hmac.New(sha256.New, salt)
	h.Write(ikm)
	prk := h.Sum(nil)

	// Expand: T(1) = HMAC-Hash(PRK, info || 0x01)
	var okm bytes.Buffer
	var prev []byte
	iteration := byte(1)

	for okm.Len() < length {
		h := hmac.New(sha256.New, prk)
		if len(prev) > 0 {
			h.Write(prev)
		}
		h.Write(info)
		h.Write([]byte{iteration})
		prev = h.Sum(nil)
		okm.Write(prev)
		iteration++
	}

	result := make([]byte, length)
	copy(result, okm.Bytes()[:length])
	return result
}

// Encrypt encrypts plaintext using AES-256-GCM and prepends the random 12-byte nonce.
// Returns standard base64-encoded string.
func Encrypt(plaintext []byte, key []byte) (string, error) {
	if len(key) != KeySize {
		return "", fmt.Errorf("invalid key size: got %d, expected %d", len(key), KeySize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create aes cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts standard base64-encoded ciphertext using AES-256-GCM.
func Decrypt(ciphertextB64 string, key []byte) ([]byte, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("invalid key size: got %d, expected %d", len(key), KeySize)
	}

	raw, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return nil, errors.New("ciphertext too short: missing nonce")
	}

	nonce := raw[:nonceSize]
	ciphertext := raw[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt ciphertext: %w", err)
	}

	return plaintext, nil
}
