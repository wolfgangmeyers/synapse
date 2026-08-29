// Package attachments owns the router's private image files. It has no HTTP
// dependencies; callers decide how attachment metadata is transported.
package attachments

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	DefaultMaxAttachments = 3
	DefaultMaxBytes       = 768 * 1024
	DefaultMaxTotalBytes  = DefaultMaxAttachments * DefaultMaxBytes
	DefaultRetention      = 24 * time.Hour
	maxImageEdge          = 16_384
	maxImagePixels        = 40_000_000
)

var (
	ErrInvalidDirectory    = errors.New("attachment directory is not private")
	ErrInvalidAttachment   = errors.New("invalid image attachment")
	ErrAttachmentTooLarge  = errors.New("image attachment is too large")
	ErrInvalidAttachmentID = errors.New("invalid attachment id")
	attachmentIDPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]+\.(?:png|jpg|webp|gif)$`)
	messageIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

type Clock interface{ Now() time.Time }

type Options struct {
	Directory      string
	Clock          Clock
	MaxAttachments int
	MaxBytes       int
	MaxTotalBytes  int
	Retention      time.Duration
	// Referenced reports whether conversation metadata still owns an attachment.
	// It is consulted for reads and expiry sweeps.
	Referenced func(string) bool
}

type Input struct {
	MIMEType string
	Name     string
	Bytes    []byte
}

type Attachment struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	MIMEType  string `json:"mime_type"`
	SizeBytes int    `json:"size_bytes"`
	Name      string `json:"name,omitempty"`
}

type Pending struct {
	Attachment
	tempPath              string
	finalPath             string
	owner                 *Store
	preexisting           bool
	preexistingReferenced bool
}

// FilePath returns the published path represented by this pending attachment.
// It is used when the message body stores the local file reference.
func (p Pending) FilePath() string { return p.finalPath }

type Store struct {
	directory      string
	clock          Clock
	maxAttachments int
	maxBytes       int
	maxTotalBytes  int
	retention      time.Duration
	referenced     func(string) bool
	chmod          func(string, os.FileMode) error
	syncDir        func(string) error
	sweepMu        sync.Mutex
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func New(options Options) (*Store, error) {
	if options.Directory == "" {
		return nil, errors.New("attachment directory is required")
	}
	directory, err := filepath.Abs(filepath.Clean(options.Directory))
	if err != nil {
		return nil, err
	}
	if directory == string(filepath.Separator) {
		return nil, ErrInvalidDirectory
	}
	maxAttachments := options.MaxAttachments
	if maxAttachments == 0 {
		maxAttachments = DefaultMaxAttachments
	}
	maxBytes := options.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	maxTotal := options.MaxTotalBytes
	if maxTotal == 0 {
		maxTotal = DefaultMaxTotalBytes
	}
	retention := options.Retention
	if retention == 0 {
		retention = DefaultRetention
	}
	if maxAttachments < 0 || maxAttachments > DefaultMaxAttachments || maxBytes <= 0 || maxTotal < maxBytes || retention <= 0 {
		return nil, errors.New("invalid attachment limits")
	}
	store := &Store{directory: directory, clock: options.Clock, maxAttachments: maxAttachments, maxBytes: maxBytes, maxTotalBytes: maxTotal, retention: retention, referenced: options.Referenced, chmod: os.Chmod, syncDir: syncDirectory}
	if store.clock == nil {
		store.clock = wallClock{}
	}
	if err := store.ensureDirectory(true); err != nil {
		return nil, err
	}
	return store, nil
}

// ValidateBatch applies the same count and aggregate limits used by admission.
func (s *Store) ValidateBatch(inputs []Input) error {
	if len(inputs) > s.maxAttachments {
		return ErrAttachmentTooLarge
	}
	total := 0
	for _, input := range inputs {
		if err := validateImage(input.MIMEType, input.Bytes, s.maxBytes); err != nil {
			return err
		}
		total += len(input.Bytes)
		if total > s.maxTotalBytes {
			return ErrAttachmentTooLarge
		}
	}
	return nil
}

// WriteTemp retains the historical method name, but publishes the file before
// returning. The caller can now safely enqueue its database row and Discard the
// published file if that transaction fails.
// WriteBatch validates count and aggregate size before publishing any files.
func (s *Store) WriteBatch(messageID string, inputs []Input) ([]Pending, error) {
	if err := s.ValidateBatch(inputs); err != nil {
		return nil, err
	}
	pending := make([]Pending, 0, len(inputs))
	for index, input := range inputs {
		item, err := s.WriteTemp(messageID, index+1, input)
		if err != nil {
			for _, created := range pending {
				_ = s.Discard(created)
			}
			return nil, err
		}
		pending = append(pending, item)
	}
	return pending, nil
}

func (s *Store) WriteTemp(messageID string, index int, input Input) (Pending, error) {
	if !messageIDPattern.MatchString(messageID) || index < 1 {
		return Pending{}, ErrInvalidAttachmentID
	}
	if err := validateImage(input.MIMEType, input.Bytes, s.maxBytes); err != nil {
		return Pending{}, err
	}
	if err := s.ensureDirectory(true); err != nil {
		return Pending{}, err
	}
	extension, ok := extensionForMIME(input.MIMEType)
	if !ok {
		return Pending{}, ErrInvalidImage
	}
	hash := sha256.Sum256(input.Bytes)
	id := fmt.Sprintf("%s-%d-%x.%s", safeMessageID(messageID), index, hash[:12], extension)
	finalPath := filepath.Join(s.directory, id)
	preexisting := false
	preexistingReferenced := false
	if info, err := os.Lstat(finalPath); err == nil {
		if !info.Mode().IsRegular() {
			return Pending{}, ErrInvalidAttachment
		}
		preexisting = true
		preexistingReferenced, _ = s.referenceState(id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Pending{}, err
	}
	file, err := os.CreateTemp(s.directory, ".attachment-*.tmp")
	if err != nil {
		return Pending{}, err
	}
	tempPath := file.Name()
	cleanup := func(e error) (Pending, error) { _ = file.Close(); _ = os.Remove(tempPath); return Pending{}, e }
	if err := file.Chmod(0o600); err != nil {
		return cleanup(err)
	}
	if _, err := file.Write(input.Bytes); err != nil {
		return cleanup(err)
	}
	if err := file.Sync(); err != nil {
		return cleanup(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return Pending{}, err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return Pending{}, err
	}
	if err := s.chmod(finalPath, 0o600); err != nil {
		if !preexisting {
			_ = os.Remove(finalPath)
		}
		return Pending{}, err
	}
	if err := s.syncDir(s.directory); err != nil {
		if !preexisting {
			_ = os.Remove(finalPath)
		}
		return Pending{}, err
	}
	return Pending{Attachment: Attachment{ID: id, URL: "/v1/attachments/" + id, MIMEType: strings.ToLower(input.MIMEType), SizeBytes: len(input.Bytes), Name: sanitizeName(input.Name)}, finalPath: finalPath, owner: s, preexisting: preexisting, preexistingReferenced: preexistingReferenced}, nil
}

// Discard removes a published attachment after enqueue failure.
func (s *Store) Discard(p Pending) error {
	if p.owner != s || (p.tempPath == "" && p.finalPath == "") {
		return ErrInvalidAttachmentID
	}
	path := p.finalPath
	if path == "" {
		path = p.tempPath
	}
	if filepath.Dir(path) != s.directory {
		return ErrInvalidAttachmentID
	}
	if p.preexisting {
		// A replacement write can belong to an idempotent message. Never let
		// compensation remove the file that was already present.
		return nil
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(s.directory)
}

// Finalize is retained for callers of the earlier API. New writes are already
// published; this method verifies that publication completed.
func (s *Store) Finalize(p Pending) error {
	if p.owner != s || !attachmentIDPattern.MatchString(p.ID) || filepath.Dir(p.finalPath) != s.directory {
		return ErrInvalidAttachmentID
	}
	if p.tempPath == "" {
		_, err := os.Stat(p.finalPath)
		return err
	}
	if err := s.ensureDirectory(false); err != nil {
		return err
	}
	if err := os.Rename(p.tempPath, p.finalPath); err != nil {
		return err
	}
	if err := os.Chmod(p.finalPath, 0o600); err != nil {
		return err
	}
	return syncDirectory(s.directory)
}

func (s *Store) Read(id string) ([]byte, Attachment, error) {
	if err := validateID(id); err != nil {
		return nil, Attachment{}, err
	}
	if err := s.ensureDirectory(false); err != nil {
		return nil, Attachment{}, err
	}
	path := filepath.Join(s.directory, id)
	link, err := os.Lstat(path)
	if err != nil {
		return nil, Attachment{}, err
	}
	info, err := os.Stat(path)
	if err != nil || !link.Mode().IsRegular() || !info.Mode().IsRegular() {
		return nil, Attachment{}, os.ErrNotExist
	}
	referenced, _ := s.referenceState(id)
	if !s.retained(info.ModTime()) && !referenced {
		return nil, Attachment{}, os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, Attachment{}, err
	}
	mime, _ := mimeForID(id)
	return data, Attachment{ID: id, URL: "/v1/attachments/" + id, MIMEType: mime, SizeBytes: len(data)}, nil
}

func (s *Store) Delete(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if err := s.ensureDirectory(false); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(s.directory, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Sweep removes unreferenced regular files older than the retention period.
// The callback is consulted while the private directory is still checked.
func (s *Store) Sweep(referenced func(string) bool) ([]string, error) {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if referenced == nil {
		referenced = s.referenced
	}
	if err := s.ensureDirectory(false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	cutoff := s.clock.Now().Add(-s.retention)
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() || (referenced != nil && referenced(entry.Name())) {
			continue
		}
		info, err := os.Stat(filepath.Join(s.directory, entry.Name()))
		if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.directory, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		removed = append(removed, entry.Name())
	}
	return removed, nil
}

func (s *Store) retained(modified time.Time) bool {
	return !modified.Before(s.clock.Now().Add(-s.retention))
}
func (s *Store) referenceState(id string) (referenced, checked bool) {
	if s.referenced == nil {
		return false, false
	}
	defer func() {
		if recover() != nil {
			referenced, checked = false, false
		}
	}()
	return s.referenced(id), true
}
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func (s *Store) ensureDirectory(create bool) error {
	if create {
		info, err := os.Lstat(s.directory)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return ErrInvalidDirectory
			}
		case errors.Is(err, os.ErrNotExist):
			if err := os.MkdirAll(s.directory, 0o700); err != nil {
				return err
			}
		default:
			return err
		}
		if err := os.Chmod(s.directory, 0o700); err != nil {
			return err
		}
	}
	info, err := os.Lstat(s.directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return ErrInvalidDirectory
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && uint32(stat.Uid) != uint32(os.Getuid()) {
		return ErrInvalidDirectory
	}
	return nil
}

func validateID(id string) error {
	if !attachmentIDPattern.MatchString(id) || filepath.Base(id) != id {
		return ErrInvalidAttachmentID
	}
	return nil
}
func safeMessageID(id string) string {
	if messageIDPattern.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%x", sum[:12])
}
func sanitizeName(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if r == ' ' || r == '.' || r == '_' || r == '-' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	value = b.String()
	if len(value) > 120 {
		value = value[:120]
	}
	return value
}

func extensionForMIME(mime string) (string, bool) {
	switch strings.ToLower(mime) {
	case "image/png":
		return "png", true
	case "image/jpeg":
		return "jpg", true
	case "image/webp":
		return "webp", true
	case "image/gif":
		return "gif", true
	}
	return "", false
}
func mimeForID(id string) (string, bool) {
	switch filepath.Ext(id) {
	case ".png":
		return "image/png", true
	case ".jpg":
		return "image/jpeg", true
	case ".webp":
		return "image/webp", true
	case ".gif":
		return "image/gif", true
	}
	return "", false
}

var ErrInvalidImage = ErrInvalidAttachment

func validateImage(mime string, data []byte, max int) error {
	if len(data) == 0 || len(data) > max {
		if len(data) > max {
			return ErrAttachmentTooLarge
		}
		return ErrInvalidImage
	}
	mime = strings.ToLower(mime)
	if _, ok := extensionForMIME(mime); !ok {
		return ErrInvalidImage
	}
	w, h, ok := imageDimensions(mime, data)
	if !ok || w > maxImageEdge || h > maxImageEdge || uint64(w)*uint64(h) > maxImagePixels {
		return ErrInvalidImage
	}
	return nil
}

func imageDimensions(mime string, b []byte) (uint32, uint32, bool) {
	switch mime {
	case "image/png":
		if len(b) < 24 || string(b[:8]) != "\x89PNG\r\n\x1a\n" || string(b[12:16]) != "IHDR" {
			return 0, 0, false
		}
		return binary.BigEndian.Uint32(b[16:20]), binary.BigEndian.Uint32(b[20:24]), true
	case "image/gif":
		if len(b) < 10 || (string(b[:6]) != "GIF87a" && string(b[:6]) != "GIF89a") {
			return 0, 0, false
		}
		return uint32(binary.LittleEndian.Uint16(b[6:8])), uint32(binary.LittleEndian.Uint16(b[8:10])), true
	case "image/jpeg":
		return jpegDimensions(b)
	case "image/webp":
		return webpDimensions(b)
	}
	return 0, 0, false
}

func jpegDimensions(b []byte) (uint32, uint32, bool) {
	if len(b) < 4 || b[0] != 0xff || b[1] != 0xd8 {
		return 0, 0, false
	}
	for off := 2; off+4 <= len(b); {
		if b[off] != 0xff {
			return 0, 0, false
		}
		for off < len(b) && b[off] == 0xff {
			off++
		}
		if off >= len(b) {
			return 0, 0, false
		}
		marker := b[off]
		off++
		if marker == 0xd8 || marker == 0xd9 {
			continue
		}
		if marker == 0xda || off+2 > len(b) {
			return 0, 0, false
		}
		length := int(binary.BigEndian.Uint16(b[off : off+2]))
		if length < 2 || off+length > len(b) {
			return 0, 0, false
		}
		if (marker >= 0xc0 && marker <= 0xc3) || (marker >= 0xc5 && marker <= 0xc7) || (marker >= 0xc9 && marker <= 0xcb) || (marker >= 0xcd && marker <= 0xcf) {
			if length < 7 {
				return 0, 0, false
			}
			return uint32(binary.BigEndian.Uint16(b[off+5 : off+7])), uint32(binary.BigEndian.Uint16(b[off+3 : off+5])), true
		}
		off += length
	}
	return 0, 0, false
}

func webpDimensions(b []byte) (uint32, uint32, bool) {
	if len(b) < 30 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" || int(binary.LittleEndian.Uint32(b[4:8]))+8 != len(b) {
		return 0, 0, false
	}
	switch string(b[12:16]) {
	case "VP8X":
		return 1 + (uint32(b[24]) | uint32(b[25])<<8 | uint32(b[26])<<16), 1 + (uint32(b[27]) | uint32(b[28])<<8 | uint32(b[29])<<16), true
	case "VP8 ":
		if len(b) >= 30 && b[23] == 0x9d && b[24] == 0x01 && b[25] == 0x2a {
			return uint32(binary.LittleEndian.Uint16(b[26:28])) & 0x3fff, uint32(binary.LittleEndian.Uint16(b[28:30])) & 0x3fff, true
		}
	case "VP8L":
		if len(b) >= 25 && b[20] == 0x2f {
			return 1 + uint32(b[21]) + uint32(b[22]&0x3f)<<8, 1 + uint32(b[22]>>6) + uint32(b[23])<<2 + uint32(b[24]&0x0f)<<10, true
		}
	}
	return 0, 0, false
}
