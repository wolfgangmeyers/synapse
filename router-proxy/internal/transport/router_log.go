package transport

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	maxRouterLogFileBytes   = 32 << 20
	maxRouterLogMatches     = 500
	maxRouterLogResponse    = 48 << 20
	maxRouterLogDiagnostics = 8
	maxRouterLogChunk       = 16 << 20
	routerLogCursorTTL      = 5 * time.Minute
)

type RouterLogQuery struct {
	Query  string
	From   *string
	To     *string
	Cursor *string
}
type RouterLogLine struct {
	BytesBase64 string `json:"bytes_base64"`
}
type RouterLogDiagnostic struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}
type RouterLogResponse struct {
	Status      string                `json:"status"`
	Lines       []RouterLogLine       `json:"lines"`
	NextCursor  *string               `json:"next_cursor"`
	Diagnostics []RouterLogDiagnostic `json:"diagnostics"`
}

var (
	errRouterLogSourceUnavailable = errors.New("router log source unavailable")
	errRouterLogInvalidCursor     = errors.New("router log cursor invalid")
)

type routerLogError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type routerLogErrorBody struct {
	Error routerLogError `json:"error"`
}

var routerLogErrorMessages = map[string]string{"invalid_request": "The router-log request is invalid.", "method_not_allowed": "The router-log method is not allowed.", "source_unavailable": "The router-log source is unavailable.", "invalid_cursor": "The router-log cursor is invalid."}

func (r *Router) handleRouterLog(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		r.writeRouterLogError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	query, err := parseRouterLogQuery(req.URL.RawQuery)
	if err != nil {
		r.writeRouterLogError(w, 400, "invalid_request")
		return
	}
	if r.routerLogReader == nil {
		r.writeRouterLogError(w, 503, "source_unavailable")
		return
	}
	response, err := r.routerLogReader.ReadRouterLog(req.Context(), query)
	if err != nil {
		code, status := "source_unavailable", 503
		if errors.Is(err, errRouterLogInvalidCursor) {
			code, status = "invalid_cursor", 400
		}
		r.writeRouterLogError(w, status, code)
		return
	}
	if err := validateRouterLogResponse(response); err != nil {
		r.writeRouterLogError(w, 503, "source_unavailable")
		return
	}
	if response.Lines == nil {
		response.Lines = []RouterLogLine{}
	}
	if response.Diagnostics == nil {
		response.Diagnostics = []RouterLogDiagnostic{}
	}
	body, err := json.Marshal(response)
	if err != nil || len(body) > maxRouterLogResponse {
		r.writeRouterLogError(w, 503, "source_unavailable")
		return
	}
	r.writeJSONHeaders(w, 200, response, map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"})
}
func validateRouterLogResponse(v RouterLogResponse) error {
	if v.Status != "complete" && v.Status != "partial" && v.Status != "source_unavailable" {
		return errors.New("invalid status")
	}
	if len(v.Lines) > maxRouterLogMatches {
		return errors.New("too many lines")
	}
	for _, l := range v.Lines {
		b, e := base64.StdEncoding.DecodeString(l.BytesBase64)
		if e != nil || len(b) > maxRouterLogFileBytes {
			return errors.New("invalid line")
		}
	}
	if len(v.Diagnostics) > maxRouterLogDiagnostics {
		return errors.New("too many diagnostics")
	}
	seen := make(map[string]bool, len(v.Diagnostics))
	for _, diagnostic := range v.Diagnostics {
		if !approvedRouterLogDiagnostic[diagnostic.Category] || diagnostic.Count < 0 || diagnostic.Count > 999 || seen[diagnostic.Category] {
			return errors.New("invalid diagnostic")
		}
		seen[diagnostic.Category] = true
	}
	return nil
}

var approvedRouterLogDiagnostic = map[string]bool{
	"source_unavailable":     true,
	"generation_unreadable":  true,
	"generation_disappeared": true,
	"snapshot_changed":       true,
	"tail_unterminated":      true,
	"cursor_invalidated":     true,
}

func (r *Router) writeRouterLogError(w http.ResponseWriter, status int, code string) {
	msg := routerLogErrorMessages[code]
	if msg == "" {
		code, msg = "invalid_request", routerLogErrorMessages["invalid_request"]
	}
	r.writeJSONHeaders(w, status, routerLogErrorBody{Error: routerLogError{Code: code, Message: msg}}, map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"})
}

func parseRouterLogQuery(raw string) (RouterLogQuery, error) {
	var result RouterLogQuery
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			return result, errors.New("empty query parameter")
		}
		pieces := strings.SplitN(part, "=", 2)
		if len(pieces) != 2 {
			return result, errors.New("invalid query parameter")
		}
		key, e := url.QueryUnescape(pieces[0])
		if e != nil || !utf8.ValidString(key) {
			return result, e
		}
		if seen[key] || (key != "query" && key != "from" && key != "to" && key != "cursor") {
			return result, errors.New("unexpected query parameter")
		}
		seen[key] = true
		value, e := url.QueryUnescape(pieces[1])
		if e != nil || !utf8.ValidString(value) {
			return result, errors.New("invalid query value")
		}
		switch key {
		case "query":
			result.Query = value
			if len([]byte(value)) > 128 || strings.IndexByte(value, 0) >= 0 {
				return result, errors.New("invalid query")
			}
		case "from", "to":
			if value == "" || len([]byte(value)) > 64 {
				return result, errors.New("invalid time")
			}
			parsed, parseErr := time.Parse(time.RFC3339, value)
			if parseErr != nil {
				return result, parseErr
			}
			value = parsed.UTC().Format(time.RFC3339Nano)
			v := value
			if key == "from" {
				result.From = &v
			} else {
				result.To = &v
			}
		case "cursor":
			if value == "" || len([]byte(value)) > 4096 {
				return result, errors.New("invalid cursor")
			}
			v := value
			result.Cursor = &v
		}
	}
	if !seen["query"] {
		return result, errors.New("query required")
	}
	if result.From != nil && result.To != nil {
		f, _ := time.Parse(time.RFC3339Nano, *result.From)
		t, _ := time.Parse(time.RFC3339Nano, *result.To)
		if !f.Before(t) {
			return result, errors.New("invalid range")
		}
	}
	return result, nil
}

func RouterLogLines(data []byte) []RouterLogLine {
	lines := make([]RouterLogLine, 0)
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, RouterLogLine{BytesBase64: base64.StdEncoding.EncodeToString(data[start:i])})
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, RouterLogLine{BytesBase64: base64.StdEncoding.EncodeToString(data[start:])})
	}
	return lines
}

type RawRouterLogReader []byte

func (r RawRouterLogReader) ReadRouterLog(context.Context, RouterLogQuery) (RouterLogResponse, error) {
	return RouterLogResponse{Status: "complete", Lines: RouterLogLines([]byte(r))}, nil
}

type configuredRouterLogReader struct {
	path string
	key  [32]byte
}

func NewConfiguredRouterLogReader(path string) (RouterLogReader, error) {
	if path == "" {
		return nil, errors.New("log path is required")
	}
	var key [32]byte
	if _, e := rand.Read(key[:]); e != nil {
		return nil, e
	}
	return &configuredRouterLogReader{path: path, key: key}, nil
}

type logFile struct {
	path       string
	name       string
	kind       string
	length     int64
	mod        int64
	dev        uint64
	ino        uint64
	generation string
	active     bool
	legacy     bool
	timestamp  time.Time
	suffix     int
	data       []byte
}
type routerLogMatch struct {
	line   []byte
	file   int
	offset int
}
type logCursor struct {
	Version          uint8
	Digest           [32]byte
	Offset           uint32
	Length           uint32
	Tail             bool
	Expires          int64
	Name             string
	Kind             string
	Dev              uint64
	Ino              uint64
	FileLen          uint32
	FileOff          uint32
	Query            string
	From             string
	To               string
	Direction        string
	BoundaryName     string
	BoundaryKind     string
	BoundaryOffset   uint32
	SnapshotBoundary uint32
	Generations      []cursorGeneration
}
type cursorGeneration struct {
	Name             string `json:"name"`
	Kind             string `json:"kind"`
	Dev              uint64 `json:"dev"`
	Ino              uint64 `json:"ino"`
	Offset           uint32 `json:"offset"`
	CapturedLength   uint32 `json:"captured_length"`
	SnapshotBoundary uint32 `json:"snapshot_boundary"`
}

func (r *configuredRouterLogReader) ReadRouterLog(ctx context.Context, q RouterLogQuery) (RouterLogResponse, error) {
	files, diags, e := r.snapshot()
	if e != nil {
		return RouterLogResponse{}, e
	}
	if len(files) == 0 {
		return RouterLogResponse{Status: "source_unavailable", Lines: []RouterLogLine{}, Diagnostics: diags}, nil
	}
	digest := snapshotDigest(files)
	pageEnd := -1 // The initial page is newest; a cursor moves toward older matches.
	if q.Cursor != nil {
		c, e := r.decode(*q.Cursor)
		if e != nil || time.Now().UnixNano() >= c.Expires || !hmac.Equal(c.Digest[:], digest[:]) || c.Query != q.Query || c.From != optional(q.From) || c.To != optional(q.To) || int(c.Offset) > int(c.Length) || !cursorSourceClaimsMatch(c, files) {
			return RouterLogResponse{}, errRouterLogInvalidCursor
		}
		pageEnd = int(c.Offset)
	}
	matches := make([]routerLogMatch, 0)
	for i := range files {
		select {
		case <-ctx.Done():
			return RouterLogResponse{}, ctx.Err()
		default:
		}
		data := files[i].data
		for _, m := range splitRawLFWithOffsets(data) {
			if !bytesContains(m.line, q.Query) || !lineInTime(m.line, q.From, q.To, &diags) {
				continue
			}
			m.file = i
			matches = append(matches, m)
		}
	}
	if q.Cursor != nil && pageEnd >= 0 {
		if c, e := r.decode(*q.Cursor); e == nil && c.Direction == "older-byte" {
			pageEnd = 0
			for i, m := range matches {
				f := files[m.file]
				if f.name == c.BoundaryName && uint32(m.offset) >= c.BoundaryOffset {
					pageEnd = i
					break
				}
			}
		}
	}
	if len(matches) > maxRouterLogMatches {
		matches = matches[len(matches)-maxRouterLogMatches:]
	}
	if pageEnd < 0 || pageEnd > len(matches) {
		pageEnd = len(matches)
	}
	status := "complete"
	if len(diags) > 0 {
		status = "partial"
	}
	var last RouterLogResponse
	haveLast := false
	for pageStart := pageEnd - 1; pageStart >= 0; pageStart-- {
		out := RouterLogResponse{Status: status, Lines: make([]RouterLogLine, 0, pageEnd-pageStart), Diagnostics: diags}
		for _, m := range matches[pageStart:pageEnd] {
			out.Lines = append(out.Lines, RouterLogLine{base64.StdEncoding.EncodeToString(m.line)})
		}
		if pageStart > 0 {
			boundary := matches[pageStart]
			c := cursorForByteBoundary(boundary, len(matches), digest, files, q)
			token, e := r.encode(c)
			if e != nil {
				return RouterLogResponse{}, e
			}
			out.NextCursor = &token
		}
		body, e := json.Marshal(out)
		if e != nil {
			return RouterLogResponse{}, e
		}
		if len(body) > maxRouterLogResponse {
			if haveLast {
				return last, nil
			}
			return RouterLogResponse{}, errors.New("router log response exceeds budget")
		}
		last = out
		haveLast = true
	}
	if !haveLast {
		return RouterLogResponse{Status: status, Lines: []RouterLogLine{}, Diagnostics: diags}, nil
	}
	return last, nil
}
func (r *configuredRouterLogReader) snapshot() ([]logFile, []RouterLogDiagnostic, error) {
	dir := filepath.Dir(r.path)
	base := filepath.Base(r.path)
	entries, e := os.ReadDir(dir)
	if e != nil {
		return nil, nil, fmt.Errorf("list log directory: %w", e)
	}
	files := make([]logFile, 0)
	diag := map[string]int{}
	for _, en := range entries {
		name := en.Name()
		if name != base && !strings.HasPrefix(name, base+".") {
			continue
		}
		if name != base && name != base+".1" && !rotationSuffix(name[len(base)+1:]) {
			if strings.HasPrefix(name, base+".") {
				diag["generation_unreadable"]++
			}
			continue
		}
		full := filepath.Join(dir, name)
		info, e := os.Lstat(full)
		if e != nil {
			if os.IsNotExist(e) {
				diag["generation_disappeared"]++
			} else {
				diag["generation_unreadable"]++
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			diag["generation_unreadable"]++
			continue
		}
		if info.Size() < 0 || info.Size() >= maxRouterLogFileBytes {
			diag["generation_unreadable"]++
			continue
		}
		data, e := readBounded(full, info.Size())
		if e != nil {
			diag["generation_unreadable"]++
			continue
		}
		endInfo, statErr := os.Lstat(full)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				diag["generation_disappeared"]++
			} else {
				diag["generation_unreadable"]++
			}
			continue
		}
		endDev, endIno := fileIdentity(endInfo)
		dev, ino := fileIdentity(info)
		if endInfo.Size() != info.Size() || endInfo.ModTime() != info.ModTime() || endDev != dev || endIno != ino {
			diag["snapshot_changed"]++
			continue
		}
		kind := "timestamped"
		stamp := time.Time{}
		suffix := 0
		if name != base && name != base+".1" {
			stamp, suffix, _ = parseRotation(name[len(base)+1:])
		}
		if name == base {
			kind = "active"
		} else if name == base+".1" {
			kind = "legacy"
		}
		files = append(files, logFile{path: full, name: name, kind: kind, length: int64(len(data)), mod: info.ModTime().UnixNano(), dev: dev, ino: ino, generation: name, data: data, active: name == base, legacy: name == base+".1", timestamp: stamp, suffix: suffix})
	}
	sort.Slice(files, func(i, j int) bool { return fileOrder(files[i], files[j]) })
	out := make([]RouterLogDiagnostic, 0, maxRouterLogDiagnostics)
	for _, k := range []string{"source_unavailable", "generation_unreadable", "generation_disappeared", "snapshot_changed", "tail_unterminated", "cursor_invalidated"} {
		if diag[k] > 0 {
			out = append(out, RouterLogDiagnostic{Category: k, Count: minDiagnostic(diag[k])})
		}
	}
	return files, out, nil
}
func minDiagnostic(n int) int {
	if n > 999 {
		return 999
	}
	return n
}
func readBounded(path string, size int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	data := make([]byte, 0, size)
	buf := make([]byte, maxRouterLogChunk)
	for int64(len(data)) < size {
		want := int64(len(buf))
		if left := size - int64(len(data)); left < want {
			want = left
		}
		n, err := io.ReadFull(f, buf[:want])
		data = append(data, buf[:n]...)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
	return data, nil
}
func fileIdentity(info os.FileInfo) (uint64, uint64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return uint64(st.Dev), uint64(st.Ino)
}
func rotationSuffix(s string) bool {
	if s == "1" {
		return true
	}
	_, _, e := parseRotation(s)
	return e == nil
}
func parseRotation(s string) (time.Time, int, error) {
	stampText, suffixText := s, "0"
	if i := strings.LastIndexByte(s, '-'); i >= 0 {
		stampText, suffixText = s[:i], s[i+1:]
	}
	stamp, e := time.Parse("20060102T150405.000000000Z", stampText)
	if e != nil {
		return time.Time{}, 0, e
	}
	var suffix int
	if _, e = fmt.Sscanf(suffixText, "%d", &suffix); e != nil || suffix < 0 || (suffixText == "0" && strings.Contains(s, "-")) {
		return time.Time{}, 0, errors.New("invalid rotation collision suffix")
	}
	return stamp, suffix, nil
}
func fileOrder(a, b logFile) bool {
	if a.active != b.active {
		return !a.active
	}
	if a.legacy != b.legacy {
		return !a.legacy
	}
	if !a.timestamp.IsZero() && !b.timestamp.IsZero() && !a.timestamp.Equal(b.timestamp) {
		return a.timestamp.Before(b.timestamp)
	}
	if a.name != b.name {
		return a.name < b.name
	}
	if a.dev != b.dev {
		return a.dev < b.dev
	}
	return a.ino < b.ino
}
func splitRawLF(data []byte) [][]byte {
	out := splitRawLFWithOffsets(data)
	lines := make([][]byte, len(out))
	for i := range out {
		lines[i] = out[i].line
	}
	return lines
}
func splitRawLFWithOffsets(data []byte) []routerLogMatch {
	out := make([]routerLogMatch, 0)
	start := 0
	for i, b := range data {
		if b == '\n' {
			out = append(out, routerLogMatch{line: data[start:i], offset: start})
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, routerLogMatch{line: data[start:], offset: start})
	}
	return out
}
func bytesContains(line []byte, q string) bool { return strings.Contains(string(line), q) }
func lineInTime(line []byte, from, to *string, diags *[]RouterLogDiagnostic) bool {
	if from == nil && to == nil {
		return true
	}
	var v struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(line, &v) != nil {
		return false
	}
	t, e := time.Parse(time.RFC3339Nano, v.Timestamp)
	if e != nil {
		return false
	}
	if from != nil {
		f, _ := time.Parse(time.RFC3339Nano, *from)
		if t.Before(f) {
			return false
		}
	}
	if to != nil {
		z, _ := time.Parse(time.RFC3339Nano, *to)
		if !t.Before(z) {
			return false
		}
	}
	return true
}
func optional(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func activeTail(fs []logFile) bool {
	for _, f := range fs {
		if f.active {
			return len(f.data) > 0 && f.data[len(f.data)-1] != '\n'
		}
	}
	return false
}

func cursorForSnapshot(offset, length int, digest [32]byte, files []logFile, q RouterLogQuery) logCursor {
	c := logCursor{Version: 1, Digest: digest, Offset: uint32(offset), Length: uint32(length), Tail: activeTail(files), Expires: time.Now().Add(routerLogCursorTTL).UnixNano(), Query: q.Query, From: optional(q.From), To: optional(q.To)}
	for _, f := range files {
		if f.active {
			c.Name, c.Kind = f.name, f.kind
			c.Dev, c.Ino = f.dev, f.ino
			c.FileLen, c.FileOff = uint32(f.length), uint32(len(f.data))
			break
		}
	}
	return c
}
func cursorForByteBoundary(m routerLogMatch, length int, digest [32]byte, files []logFile, q RouterLogQuery) logCursor {
	c := cursorForSnapshot(0, length, digest, files, q)
	f := files[m.file]
	c.Direction = "older-byte"
	c.BoundaryName, c.BoundaryKind = f.name, f.kind
	c.BoundaryOffset = uint32(m.offset)
	c.SnapshotBoundary = uint32(f.length)
	c.Generations = make([]cursorGeneration, len(files))
	for i, g := range files {
		c.Generations[i] = cursorGeneration{Name: g.name, Kind: g.kind, Dev: g.dev, Ino: g.ino, Offset: uint32(len(g.data)), CapturedLength: uint32(g.length), SnapshotBoundary: uint32(g.length)}
	}
	return c
}

func cursorSourceClaimsMatch(c logCursor, files []logFile) bool {
	if c.Direction == "older-byte" {
		if len(c.Generations) != len(files) {
			return false
		}
		for i, g := range files {
			if g.name != c.Generations[i].Name || g.kind != c.Generations[i].Kind || g.dev != c.Generations[i].Dev || g.ino != c.Generations[i].Ino || uint32(g.length) != c.Generations[i].CapturedLength || uint32(len(g.data)) != c.Generations[i].Offset || c.Generations[i].SnapshotBoundary != uint32(g.length) {
				return false
			}
		}
		if c.SnapshotBoundary == 0 || c.BoundaryOffset > c.SnapshotBoundary {
			return false
		}
		for _, f := range files {
			if f.name == c.BoundaryName {
				return f.kind == c.BoundaryKind && uint32(f.length) == c.SnapshotBoundary
			}
		}
		return false
	}
	for _, f := range files {
		if f.active {
			return c.Name == f.name && c.Kind == f.kind && c.Dev == f.dev && c.Ino == f.ino && c.FileLen == uint32(f.length) && c.FileOff == uint32(len(f.data)) && c.Tail == activeTail(files)
		}
	}
	return c.Name == "" && c.Kind == "" && c.Dev == 0 && c.Ino == 0 && c.FileLen == 0 && c.FileOff == 0 && !c.Tail
}
func snapshotDigest(fs []logFile) [32]byte {
	h := sha256.New()
	for _, f := range fs {
		h.Write([]byte(f.name + "\x00" + f.kind))
		var b [32]byte
		binary.BigEndian.PutUint64(b[:8], uint64(f.length))
		binary.BigEndian.PutUint64(b[8:16], uint64(f.mod))
		binary.BigEndian.PutUint64(b[16:24], f.dev)
		binary.BigEndian.PutUint64(b[24:], f.ino)
		h.Write(b[:])
		content := sha256.Sum256(f.data)
		h.Write(content[:])
		binary.BigEndian.PutUint64(b[:8], uint64(len(f.data)))
		h.Write(b[:8])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
func (r *configuredRouterLogReader) encode(c logCursor) (string, error) {
	b := make([]byte, 0, 256)
	b = append(b, c.Version)
	b = append(b, c.Digest[:]...)
	var x [8]byte
	binary.BigEndian.PutUint32(x[:4], c.Offset)
	binary.BigEndian.PutUint32(x[4:], c.Length)
	b = append(b, x[:]...)
	binary.BigEndian.PutUint64(x[:], uint64(c.Expires))
	b = append(b, x[:]...)
	binary.BigEndian.PutUint64(x[:], c.Dev)
	b = append(b, x[:]...)
	binary.BigEndian.PutUint64(x[:], c.Ino)
	b = append(b, x[:]...)
	binary.BigEndian.PutUint32(x[:4], c.FileLen)
	binary.BigEndian.PutUint32(x[4:], c.FileOff)
	b = append(b, x[:]...)
	if c.Tail {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	generations, _ := json.Marshal(c.Generations)
	b = append(b, []byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%s", c.Name, c.Kind, c.Query, c.From, c.To, c.Direction, c.BoundaryName, c.BoundaryKind, c.BoundaryOffset, c.SnapshotBoundary, generations))...)
	mac := hmac.New(sha256.New, r.key[:])
	mac.Write(b)
	b = append(b, mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (r *configuredRouterLogReader) decode(s string) (logCursor, error) {
	b, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil || len(b) < 74+32 {
		return logCursor{}, errRouterLogInvalidCursor
	}
	mac := hmac.New(sha256.New, r.key[:])
	mac.Write(b[:len(b)-32])
	if !hmac.Equal(b[len(b)-32:], mac.Sum(nil)) {
		return logCursor{}, errRouterLogInvalidCursor
	}
	var c logCursor
	c.Version = b[0]
	copy(c.Digest[:], b[1:33])
	c.Offset = binary.BigEndian.Uint32(b[33:37])
	c.Length = binary.BigEndian.Uint32(b[37:41])
	c.Expires = int64(binary.BigEndian.Uint64(b[41:49]))
	c.Dev = binary.BigEndian.Uint64(b[49:57])
	c.Ino = binary.BigEndian.Uint64(b[57:65])
	c.FileLen = binary.BigEndian.Uint32(b[65:69])
	c.FileOff = binary.BigEndian.Uint32(b[69:73])
	c.Tail = b[73] != 0
	p := strings.Split(string(b[74:len(b)-32]), "\x00")
	if c.Version != 1 || (len(p) != 5 && len(p) != 11) {
		return logCursor{}, errRouterLogInvalidCursor
	}
	c.Name, c.Kind, c.Query, c.From, c.To = p[0], p[1], p[2], p[3], p[4]
	if len(p) == 11 {
		c.Direction, c.BoundaryName, c.BoundaryKind = p[5], p[6], p[7]
		if _, e := fmt.Sscanf(p[8], "%d", &c.BoundaryOffset); e != nil {
			return logCursor{}, errRouterLogInvalidCursor
		}
		if _, e := fmt.Sscanf(p[9], "%d", &c.SnapshotBoundary); e != nil {
			return logCursor{}, errRouterLogInvalidCursor
		}
		if e := json.Unmarshal([]byte(p[10]), &c.Generations); e != nil {
			return logCursor{}, errRouterLogInvalidCursor
		}
		if c.Direction == "older-byte" && c.BoundaryName == "" {
			return logCursor{}, errRouterLogInvalidCursor
		}
	}
	return c, nil
}
