package managedbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	MaxConfigBytes      = 128 * 1024
	MaxTokenBytes       = 128
	MaxArgs             = 64
	MaxEnvironment      = 128
	MaxArgumentBytes    = 4096
	MaxEnvironmentBytes = 4096
	MaxAgentBytes       = 64
	MaxReconnectInitial = 100 * time.Millisecond
	MaxReconnectDelay   = 5 * time.Second
)

// Config is the standalone Phase 1 bridge configuration. It is deliberately
// independent from router and supervisor configuration. Token is read from
// TokenPath and is never stored in Config.
type Config struct {
	RouterURL    string   `json:"router_url"`
	TokenPath    string   `json:"token_path"`
	Agent        string   `json:"agent"`
	PiExecutable string   `json:"pi_executable"`
	PiArgs       []string `json:"pi_args"`
	PiEnv        []string `json:"pi_env"`
	PiDir        string   `json:"pi_dir"`

	MaxStdoutLine    int           `json:"max_stdout_line"`
	MaxStderrBytes   int           `json:"max_stderr_bytes"`
	OutboxCapacity   int           `json:"outbox_capacity"`
	CommandWindow    int           `json:"command_window"`
	ReconnectInitial time.Duration `json:"reconnect_initial"`
	ReconnectMax     time.Duration `json:"reconnect_max"`
}

// String and GoString intentionally omit every value that could identify a
// local file, executable, environment, URL, or runtime argument.
func (Config) String() string   { return "managed bridge config [redacted]" }
func (Config) GoString() string { return "managed bridge config [redacted]" }

func defaultConfig(c Config) Config {
	if c.MaxStdoutLine == 0 {
		c.MaxStdoutLine = MaxPayloadBytes
	}
	if c.MaxStderrBytes == 0 {
		c.MaxStderrBytes = 16 * 1024
	}
	if c.OutboxCapacity == 0 {
		c.OutboxCapacity = 128
	}
	if c.CommandWindow == 0 {
		c.CommandWindow = 128
	}
	if c.ReconnectInitial == 0 {
		c.ReconnectInitial = MaxReconnectInitial
	}
	if c.ReconnectMax == 0 {
		c.ReconnectMax = MaxReconnectDelay
	}
	return c
}

func absolutePath(value string) bool {
	return value != "" && len(value) <= MaxArgumentBytes && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func (c Config) validate() error {
	c = defaultConfig(c)
	if validateRouterURL(c.RouterURL) != nil || !absolutePath(c.TokenPath) || c.Agent == "" || !absolutePath(c.PiExecutable) || !absolutePath(c.PiDir) {
		return ErrInvalidConfig
	}
	if len(c.Agent) > MaxAgentBytes || !validIdentity(c.Agent) {
		return ErrInvalidConfig
	}
	if len(c.PiArgs) > MaxArgs || len(c.PiEnv) > MaxEnvironment || c.MaxStdoutLine < 1 || c.MaxStdoutLine > MaxPayloadBytes || c.MaxStderrBytes < 1 || c.MaxStderrBytes > 1<<20 || c.OutboxCapacity < 1 || c.OutboxCapacity > MaxCursorCapacity || c.CommandWindow < 1 || c.CommandWindow > MaxCursorCapacity || c.ReconnectInitial <= 0 || c.ReconnectInitial > MaxReconnectDelay || c.ReconnectMax < c.ReconnectInitial || c.ReconnectMax > MaxReconnectDelay {
		return ErrInvalidConfig
	}
	for _, arg := range c.PiArgs {
		if len(arg) == 0 || len(arg) > MaxArgumentBytes {
			return ErrInvalidConfig
		}
	}
	for _, env := range c.PiEnv {
		if len(env) == 0 || len(env) > MaxEnvironmentBytes || !strings.Contains(env, "=") {
			return ErrInvalidConfig
		}
	}
	return nil
}

func validateRouterURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ws" || u.Opaque != "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1/managed-bridge" || u.RawPath != "" || u.Host == "" || u.Hostname() == "" {
		return ErrInvalidConfig
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return ErrInvalidConfig
	}
	if strings.Contains(u.Host, "%") {
		return ErrInvalidConfig
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return ErrInvalidConfig
	}
	return nil
}

func currentOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint32(stat.Uid) == uint32(os.Getuid())
}

func openSecureParent(path string) (int, error) {
	parent := filepath.Dir(path)
	// macOS exposes /var and /tmp through system symlinks. Resolve those
	// aliases first, then open the resolved directory without following its
	// final component. The caller opens the file itself with openat, so a later
	// pathname replacement cannot redirect the read.
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return -1, err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 {
		return -1, ErrInvalidConfig
	}
	fd, err := unix.Open(resolved, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Getuid()) || stat.Mode&0022 != 0 {
		_ = unix.Close(fd)
		return -1, ErrInvalidConfig
	}
	return fd, nil
}

// load0600 opens every directory component and the final file without
// following symlinks. The mode/owner check stays tied to the opened descriptor
// even if the pathname is replaced concurrently.
func load0600(path string, limit int) ([]byte, error) {
	if !absolutePath(path) {
		return nil, ErrInvalidConfig
	}
	parent, err := openSecureParent(path)
	if err != nil {
		return nil, ErrInvalidConfig
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, filepath.Base(path), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrInvalidConfig
	}
	file := os.NewFile(uintptr(fd), "")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrInvalidConfig
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !currentOwner(info) {
		return nil, ErrInvalidConfig
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, ErrInvalidConfig
	}
	return data, nil
}

// LoadConfig reads a bounded mode-0600 JSON file and its separate mode-0600
// token file. The returned token is verifier material for NewBridgeFromFiles;
// callers must not log or retain it.
func LoadConfig(path string) (Config, []byte, error) {
	data, err := load0600(path, MaxConfigBytes)
	if err != nil {
		return Config{}, nil, ErrInvalidConfig
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, nil, ErrInvalidConfig
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || config.validate() != nil {
		return Config{}, nil, ErrInvalidConfig
	}
	token, err := load0600(config.TokenPath, MaxTokenBytes)
	if err != nil {
		return Config{}, nil, ErrInvalidConfig
	}
	if len(token) == 64 {
		decoded, decodeErr := hex.DecodeString(string(token))
		if decodeErr == nil {
			token = decoded
		}
	}
	if len(token) != 32 || bytes.Equal(token, make([]byte, 32)) {
		return Config{}, nil, ErrInvalidConfig
	}
	return defaultConfig(config), token, nil
}

// NewBridgeFromFiles loads the standalone configuration and immediately
// derives the verifier. Token bytes are discarded after derivation.
func NewBridgeFromFiles(path string, options Options) (*Bridge, error) {
	config, token, err := LoadConfig(path)
	if err != nil {
		return nil, ErrInvalidConfig
	}
	verifier, err := NewVerifier(token)
	for index := range token {
		token[index] = 0
	}
	if err != nil {
		return nil, ErrInvalidConfig
	}
	return NewBridge(config, verifier, options)
}

func manifestHash() string {
	sum := sha256.Sum256([]byte("mecha-managed-rpc-phase1"))
	return hex.EncodeToString(sum[:])
}

func validateConfigForTests(c Config) error { return c.validate() }
