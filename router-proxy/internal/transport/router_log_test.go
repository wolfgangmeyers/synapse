package transport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRouterLogQueryUsesExplicitMapping(t *testing.T) {
	from := "2026-01-01T00:00:00Z"
	to := "2026-01-02T00:00:00Z"
	query, err := parseRouterLogQuery("query=raw+line&from=" + from + "&to=" + to + "&cursor=opaque")
	require.NoError(t, err)
	require.Equal(t, "raw line", query.Query)
	require.Equal(t, &from, query.From)
	require.Equal(t, &to, query.To)
	require.Equal(t, "opaque", *query.Cursor)

	for _, raw := range []string{"from=x", "query=a&query=b", "query=a&extra=b", "query=a%", "query=%ff", "query=a&from=", "query=a&cursor="} {
		_, err := parseRouterLogQuery(raw)
		require.Error(t, err, raw)
	}
}

func TestRouterLogEndpointReturnsClosedFixedErrors(t *testing.T) {
	router := &Router{ids: randomIDs{}, routerLogReader: RouterLogReaderFunc(func(context.Context, RouterLogQuery) (RouterLogResponse, error) {
		return RouterLogResponse{}, errRouterLogInvalidCursor
	})}
	for _, test := range []struct {
		name, method, target  string
		status, code, message int
	}{
		{name: "missing query", method: http.MethodGet, target: "/v1/router-log", status: 400},
		{name: "method", method: http.MethodPost, target: "/v1/router-log?query=", status: 405},
		{name: "cursor", method: http.MethodGet, target: "/v1/router-log?query=x&cursor=opaque", status: 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.handleHTTP(recorder, httptest.NewRequest(test.method, test.target, nil))
			require.Equal(t, test.status, recorder.Code)
			var body map[string]map[string]string
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			require.Len(t, body, 1)
			require.Contains(t, body, "error")
			require.Len(t, body["error"], 2)
			require.NotContains(t, recorder.Body.String(), "opaque")
		})
	}

	router.routerLogReader = nil
	recorder := httptest.NewRecorder()
	router.handleHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/router-log?query=", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"code":"source_unavailable"`)
	require.NotContains(t, recorder.Body.String(), "path")
}

func TestRouterLogEndpointReturnsClosedResponseAndPreservesRawLines(t *testing.T) {
	raw := []byte("first\r\n\xff\r\n\nlast")
	router := &Router{ids: randomIDs{}, routerLogReader: RawRouterLogReader(raw)}
	recorder := httptest.NewRecorder()
	router.handleHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/router-log?query=", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	var response RouterLogResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, "complete", response.Status)
	require.Len(t, response.Lines, 4)
	require.Equal(t, "first\r", decodeLine(t, response.Lines[0]))
	require.Equal(t, []byte{0xff, '\r'}, decodeLineBytes(t, response.Lines[1]))
	require.Equal(t, []byte{}, decodeLineBytes(t, response.Lines[2]))
	require.Equal(t, "last", decodeLine(t, response.Lines[3]))
	require.Empty(t, response.Diagnostics)
}

func TestParseRouterLogQueryRejectsOversizedCursor(t *testing.T) {
	_, err := parseRouterLogQuery("query=x&cursor=" + url.QueryEscape(strings.Repeat("a", maxRouterLogCursor+1)))
	require.Error(t, err)
}

func TestConfiguredRouterLogSnapshotRejectsSymlinkEvenWhenTargetIsSameInode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	target := filepath.Join(dir, "target.jsonl")
	require.NoError(t, os.WriteFile(target, []byte("secret\n"), 0o600))
	require.NoError(t, os.Symlink(target, path))
	targetInfo, err := os.Stat(target)
	require.NoError(t, err)
	pathInfo, err := os.Stat(path)
	require.NoError(t, err)
	targetDev, targetIno := fileIdentity(targetInfo)
	pathDev, pathIno := fileIdentity(pathInfo)
	require.Equal(t, targetDev, pathDev)
	require.Equal(t, targetIno, pathIno)
	directory, err := os.Open(dir)
	require.NoError(t, err)
	defer directory.Close()
	_, err = openRouterLogFile(directory, "router.jsonl")
	require.Error(t, err)
	rawReader, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	response, err := rawReader.ReadRouterLog(context.Background(), RouterLogQuery{Query: ""})
	require.NoError(t, err)
	require.Empty(t, response.Lines)
	require.Contains(t, response.Diagnostics, RouterLogDiagnostic{Category: "generation_unreadable", Count: 1})
}

func TestConfiguredRouterLogReaderMarshals500LargeLinesWithinResponseBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	line := bytes.Repeat([]byte{'x'}, 70<<10)
	for i := 0; i < 500; i++ {
		name := fmt.Sprintf("router.jsonl.20260101T000000.000000000Z-%d", i+1)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), append(append([]byte{}, line...), '\n'), 0o600))
	}
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	r, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	reader := r.(*configuredRouterLogReader)
	base64Encodes, lineMarshals, envelopeMarshals, cursorEncodes := 0, 0, 0, 0
	defaultEncodeLine, defaultMarshalLine := reader.encodeLine, reader.marshalLine
	defaultMarshalEnvelope, defaultEncodeCursor := reader.marshalEnvelope, reader.encodeCursor
	reader.encodeLine = func(value []byte) string { base64Encodes++; return defaultEncodeLine(value) }
	reader.marshalLine = func(value any) ([]byte, error) { lineMarshals++; return defaultMarshalLine(value) }
	reader.marshalEnvelope = func(value any) ([]byte, error) { envelopeMarshals++; return defaultMarshalEnvelope(value) }
	reader.encodeCursor = func(value logCursor) (string, error) { cursorEncodes++; return defaultEncodeCursor(value) }
	response, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x"})
	require.NoError(t, err)
	require.Len(t, response.Lines, 500)
	require.Equal(t, 500, base64Encodes)
	require.Equal(t, 500, lineMarshals)
	require.Equal(t, 500, envelopeMarshals)
	require.Equal(t, 499, cursorEncodes)
	require.LessOrEqual(t, cursorEncodes, maxRouterLogMatches)
	require.Nil(t, response.NextCursor)
	require.Empty(t, response.Diagnostics)
}

func TestConfiguredRouterLogReaderContinuesWith22Generations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	line := strings.Repeat("x", 3<<20)
	for i := 0; i < 22; i++ {
		name := fmt.Sprintf("router.jsonl.202601%02dT000000.000000000Z", i+1)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(line+"\n"), 0o600))
	}
	require.NoError(t, os.WriteFile(path, []byte(line+"\n"), 0o600))
	r, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	first, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x"})
	require.NoError(t, err)
	require.NotNil(t, first.NextCursor)
	require.LessOrEqual(t, len(*first.NextCursor), maxRouterLogCursor)
	second, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x", Cursor: first.NextCursor})
	require.NoError(t, err)
	require.NotEmpty(t, second.Lines)
}

func TestConfiguredRouterLogReaderGenerationsSearchAndTime(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "router.jsonl")
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	write("router.jsonl.20260101T000000.000000000Z", `{"timestamp":"2026-01-01T00:00:00Z","event":"old"}`+"\n")
	write("router.jsonl.20260102T000000.000000000Z", `{"timestamp":"2026-01-02T00:00:00Z","event":"middle"}`+"\n")
	write("router.jsonl.1", `{"timestamp":"2026-01-03T00:00:00Z","event":"legacy"}`+"\n")
	write("router.jsonl", "raw\r\n\xff\n")
	r, err := NewConfiguredRouterLogReader(active)
	require.NoError(t, err)
	response, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: ""})
	require.NoError(t, err)
	require.Equal(t, []string{`{"timestamp":"2026-01-01T00:00:00Z","event":"old"}`, `{"timestamp":"2026-01-02T00:00:00Z","event":"middle"}`, `{"timestamp":"2026-01-03T00:00:00Z","event":"legacy"}`, "raw\r", "\xff"}, func() []string {
		out := make([]string, 0, len(response.Lines))
		for _, line := range response.Lines {
			out = append(out, string(decodeLineBytes(t, line)))
		}
		return out
	}())
	from, to := "2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z"
	response, err = r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "event", From: &from, To: &to})
	require.NoError(t, err)
	require.Len(t, response.Lines, 1)
	require.Contains(t, string(decodeLineBytes(t, response.Lines[0])), "middle")
}

func TestConfiguredRouterLogReaderRetainsNewest500Chronologically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	var b strings.Builder
	for i := 0; i < 510; i++ {
		fmt.Fprintf(&b, `{"event":"line-%03d"}`+"\n", i)
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	r, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	response, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "line"})
	require.NoError(t, err)
	require.Len(t, response.Lines, 500)
	require.Nil(t, response.NextCursor)
	for i, line := range response.Lines {
		require.Equal(t, fmt.Sprintf(`{"event":"line-%03d"}`, i+10), decodeLine(t, line))
	}
}

func TestConfiguredRouterLogReaderOrdersTimestampedGenerationTies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	for name, event := range map[string]string{
		"router.jsonl.20260101T000000.000000000Z-2": "b",
		"router.jsonl.20260101T000000.000000000Z-1": "a",
		"router.jsonl.20260102T000000.000000000Z":   "next",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(event+"\n"), 0o600))
	}
	require.NoError(t, os.WriteFile(path, []byte("active\n"), 0o600))
	r, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	response, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: ""})
	require.NoError(t, err)
	got := make([]string, 0, len(response.Lines))
	for _, line := range response.Lines {
		got = append(got, decodeLine(t, line))
	}
	require.Equal(t, []string{"a", "b", "next", "active"}, got)
}

func TestRouterLogRotationSuffixRequiresNumericCollision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("active\n"), 0o600))
	require.NoError(t, os.WriteFile(path+".20260101T000000.000000000Z-x", []byte("bad\n"), 0o600))
	r, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	response, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: ""})
	require.NoError(t, err)
	require.Equal(t, []string{"active"}, []string{decodeLine(t, response.Lines[0])})
	require.Contains(t, response.Diagnostics, RouterLogDiagnostic{Category: "generation_unreadable", Count: 1})
}

func TestRouterLogResponseContinuationUsesRawLineBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	rotated := path + ".20260101T000000.000000000Z"
	first := bytes.Repeat([]byte{0xff}, 24<<20)
	second := bytes.Repeat([]byte{0x00}, 24<<20)
	require.NoError(t, os.WriteFile(rotated, append(append([]byte{}, first...), '\n'), 0o600))
	require.NoError(t, os.WriteFile(path, append(append([]byte{}, second...), '\n'), 0o600))
	rawReader, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	response, err := rawReader.ReadRouterLog(context.Background(), RouterLogQuery{Query: ""})
	require.NoError(t, err)
	require.Len(t, response.Lines, 1)
	require.NotNil(t, response.NextCursor)
	require.Equal(t, second, decodeLineBytes(t, response.Lines[0]))
	continued, err := rawReader.ReadRouterLog(context.Background(), RouterLogQuery{Query: "", Cursor: response.NextCursor})
	require.NoError(t, err)
	require.Equal(t, first, decodeLineBytes(t, continued.Lines[0]))
}

func TestRouterLogReassemblesLineLargerThan16MiBReadChunk(t *testing.T) {
	line := bytes.Repeat([]byte{'a'}, maxRouterLogChunk+1)
	data := append(append([]byte{}, line...), '\n')
	data = append(data, []byte("\xff\n")...)
	lines := splitRawLF(data)
	require.Equal(t, [][]byte{line, []byte{0xff}}, lines)
}

func decodeLine(t *testing.T, line RouterLogLine) string { return string(decodeLineBytes(t, line)) }
func decodeLineBytes(t *testing.T, line RouterLogLine) []byte {
	decoded, err := base64.StdEncoding.DecodeString(line.BytesBase64)
	require.NoError(t, err)
	return decoded
}

func TestConfiguredRouterLogReaderDoesNotMixRecordsAfterSnapshotChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	raw := []byte("first\r\n\xff\nsecond\n")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	rawReader, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	r := rawReader.(*configuredRouterLogReader)

	response, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: ""})
	require.NoError(t, err)
	require.Equal(t, []RouterLogLine{
		{BytesBase64: base64.StdEncoding.EncodeToString([]byte("first\r"))},
		{BytesBase64: base64.StdEncoding.EncodeToString([]byte{0xff})},
		{BytesBase64: base64.StdEncoding.EncodeToString([]byte("second"))},
	}, response.Lines)
	require.Equal(t, []byte("first\r"), decodeLineBytes(t, response.Lines[0]))
	require.Equal(t, []byte{0xff}, decodeLineBytes(t, response.Lines[1]))

	files, _, err := r.snapshot()
	require.NoError(t, err)
	digest := snapshotDigest(files)
	cursor, err := r.encode(cursorForSnapshot(1, 3, digest, files, RouterLogQuery{Query: ""}))
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.Write([]byte("newly-appended\n"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	response, err = r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "", Cursor: &cursor})
	require.ErrorIs(t, err, errRouterLogInvalidCursor)
	require.Empty(t, response.Lines)
	require.Equal(t, raw, files[0].data)
}

func TestConfiguredRouterLogReaderCursorContinuesWithRawBytes(t *testing.T) {
	r, _, _, _, second, cursor := newCursorReader(t)
	decoded, err := r.(*configuredRouterLogReader).decode(cursor)
	require.NoError(t, err)
	require.True(t, decoded.Tail)
	response, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x", Cursor: &cursor})
	require.NoError(t, err)
	require.Equal(t, []RouterLogLine{{BytesBase64: base64.StdEncoding.EncodeToString(second)}}, response.Lines)
	require.Nil(t, response.NextCursor)
}

func TestConfiguredRouterLogReaderRejectsExpiredAndTamperedCursors(t *testing.T) {
	r, _, _, _, _, cursor := newCursorReader(t)
	configured := r.(*configuredRouterLogReader)
	expired, err := configured.decode(cursor)
	require.NoError(t, err)
	expired.Expires = time.Now().Add(-time.Second).UnixNano()
	expiredToken, err := configured.encode(expired)
	require.NoError(t, err)
	_, err = r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x", Cursor: &expiredToken})
	require.ErrorIs(t, err, errRouterLogInvalidCursor)

	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	require.NoError(t, err)
	raw[len(raw)-1] ^= 1
	tampered := base64.RawURLEncoding.EncodeToString(raw)
	_, err = r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x", Cursor: &tampered})
	require.ErrorIs(t, err, errRouterLogInvalidCursor)
}

func TestConfiguredRouterLogReaderInvalidatesCursorWhenSourceChanges(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, dir, active, rotated string)
	}{
		{name: "replacement", mutate: func(t *testing.T, dir, active, rotated string) {
			require.NoError(t, os.Remove(active))
			require.NoError(t, os.WriteFile(active, []byte(strings.Repeat("x", 20<<20)), 0o600))
		}},
		{name: "removal", mutate: func(t *testing.T, dir, active, rotated string) {
			require.NoError(t, os.Remove(active))
		}},
		{name: "length change", mutate: func(t *testing.T, dir, active, rotated string) {
			f, err := os.OpenFile(active, os.O_APPEND|os.O_WRONLY, 0)
			require.NoError(t, err)
			_, err = f.Write([]byte("x"))
			require.NoError(t, err)
			require.NoError(t, f.Close())
		}},
		{name: "new generation", mutate: func(t *testing.T, dir, active, rotated string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "router.jsonl.20260103T000000.000000000Z"), []byte("x"), 0o600))
		}},
		{name: "tail completion", mutate: func(t *testing.T, dir, active, rotated string) {
			f, err := os.OpenFile(active, os.O_APPEND|os.O_WRONLY, 0)
			require.NoError(t, err)
			_, err = f.Write([]byte("\n"))
			require.NoError(t, err)
			require.NoError(t, f.Close())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, dir, active, rotated, _, cursor := newCursorReader(t)
			tc.mutate(t, dir, active, rotated)
			_, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x", Cursor: &cursor})
			require.ErrorIs(t, err, errRouterLogInvalidCursor)
		})
	}
}

func TestConfiguredRouterLogReaderAcceptsNear32MiBLineInEligibleGeneration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	line := bytes.Repeat([]byte{'x'}, maxRouterLogFileBytes-2)
	require.NoError(t, os.WriteFile(path, append(append([]byte{}, line...), '\n'), 0o600))
	r, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	response, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x"})
	require.NoError(t, err)
	require.Equal(t, [][]byte{line}, func() [][]byte {
		out := make([][]byte, 0, len(response.Lines))
		for _, got := range response.Lines {
			out = append(out, decodeLineBytes(t, got))
		}
		return out
	}())
	require.Empty(t, response.Diagnostics)
}

func TestConfiguredRouterLogReaderRejectsGenerationAt32MiB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{'x'}, maxRouterLogFileBytes), 0o600))
	r, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	response, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x"})
	require.NoError(t, err)
	require.Empty(t, response.Lines)
	require.Contains(t, response.Diagnostics, RouterLogDiagnostic{Category: "generation_unreadable", Count: 1})
}

func TestConfiguredRouterLogReaderResponseBudgetAllowsOneMaximumSizeLineAndEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	line := []byte(strings.Repeat("x", maxRouterLogFileBytes-1))
	encoded := base64.StdEncoding.EncodeToString(line)
	require.Equal(t, 44_739_244, len(encoded))
	require.NoError(t, os.WriteFile(path, line, 0o600))
	rawReader, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	response, err := rawReader.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x"})
	require.NoError(t, err)
	require.Equal(t, []RouterLogLine{{BytesBase64: encoded}}, response.Lines)
	require.Equal(t, line, decodeLineBytes(t, response.Lines[0]))
	require.Nil(t, response.NextCursor)
	body, err := json.Marshal(response)
	require.NoError(t, err)
	require.LessOrEqual(t, len(body), maxRouterLogResponse)
	require.Less(t, len(body), 48<<20)
}

func newCursorReader(t *testing.T) (RouterLogReader, string, string, string, []byte, string) {
	t.Helper()
	dir := t.TempDir()
	active := filepath.Join(dir, "router.jsonl")
	rotated := filepath.Join(dir, "router.jsonl.20260101T000000.000000000Z")
	first := []byte(strings.Repeat("x", 20<<20))
	second := []byte(strings.Repeat("x", 20<<20))
	require.NoError(t, os.WriteFile(rotated, append(append([]byte{}, second...), '\n'), 0o600))
	require.NoError(t, os.WriteFile(active, first, 0o600))
	rawReader, err := NewConfiguredRouterLogReader(active)
	require.NoError(t, err)
	r := rawReader.(*configuredRouterLogReader)
	files, _, err := r.snapshot()
	require.NoError(t, err)
	digest := snapshotDigest(files)
	cursor, err := r.encode(cursorForSnapshot(1, 2, digest, files, RouterLogQuery{Query: "x"}))
	require.NoError(t, err)
	return r, dir, active, rotated, second, cursor
}
