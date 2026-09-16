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
	"runtime"
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
			router.handleHTTP(recorder, localRouterLogRequest(test.method, test.target))
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
	router.handleHTTP(recorder, localRouterLogRequest(http.MethodGet, "/v1/router-log?query="))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"code":"source_unavailable"`)
	require.NotContains(t, recorder.Body.String(), "path")
}

func TestRouterLogEndpointRejectsUnboundedResponseCursor(t *testing.T) {
	cursor := strings.Repeat("a", maxRouterLogCursor+1)
	router := &Router{ids: randomIDs{}, routerLogReader: RouterLogReaderFunc(func(context.Context, RouterLogQuery) (RouterLogResponse, error) {
		return RouterLogResponse{Status: "complete", Lines: []RouterLogLine{}, NextCursor: &cursor, Diagnostics: []RouterLogDiagnostic{}}, nil
	})}
	recorder := httptest.NewRecorder()
	router.handleHTTP(recorder, localRouterLogRequest(http.MethodGet, "/v1/router-log?query="))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"code":"source_unavailable"`)
	require.NotContains(t, recorder.Body.String(), cursor)
}

func TestRouterLogEndpointWritesExactlyTheValidatedResponseBytes(t *testing.T) {
	const (
		query = "qqqq"
		from  = "2026-01-01T00:00:00Z"
		to    = "2026-01-04T00:00:00Z"
	)
	line := func(timestamp string, length int) []byte {
		prefix := []byte(`{"timestamp":"` + timestamp + `","event":"` + query)
		prefix = append(prefix, strings.Repeat("q", length-len(prefix)-2)...)
		return append(prefix, []byte(`"}`)...)
	}
	old := line("2026-01-01T00:00:00Z", 300)
	middle := line("2026-01-02T00:00:00Z", 4_193_943)
	active := line("2026-01-03T00:00:00Z", 33_554_430)
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	files := []struct {
		name string
		line []byte
	}{
		{"router.jsonl.20260101T000000.000000000Z", old},
		{"router.jsonl.20260102T000000.000000000Z", middle},
		{"router.jsonl", active},
	}
	for _, file := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, file.name), append(append([]byte{}, file.line...), '\n'), 0o600))
	}

	rawReader, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	reader := rawReader.(*configuredRouterLogReader)
	snapshot, diagnostics, err := reader.snapshot()
	require.NoError(t, err)
	require.Empty(t, diagnostics)
	require.Len(t, snapshot, len(files))
	for i, file := range files {
		info, statErr := os.Stat(filepath.Join(dir, file.name))
		require.NoError(t, statErr)
		require.True(t, info.Mode().IsRegular())
		require.Equal(t, int64(len(file.line)+1), info.Size())
		require.Equal(t, file.name, snapshot[i].name)
		require.Equal(t, len(file.line)+1, len(snapshot[i].data))
		require.True(t, json.Valid(file.line))
		require.Contains(t, string(file.line), query)
		var record struct {
			Timestamp string `json:"timestamp"`
		}
		require.NoError(t, json.Unmarshal(file.line, &record))
		stamp, parseErr := time.Parse(time.RFC3339Nano, record.Timestamp)
		require.NoError(t, parseErr)
		fromTime, _ := time.Parse(time.RFC3339Nano, from)
		toTime, _ := time.Parse(time.RFC3339Nano, to)
		require.False(t, stamp.Before(fromTime) || !stamp.Before(toTime))
	}

	requestTarget := "/v1/router-log?query=" + query + "&from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to)
	router := &Router{ids: randomIDs{}, routerLogReader: rawReader}
	recorder := httptest.NewRecorder()
	router.handleHTTP(recorder, localRouterLogRequest(http.MethodGet, requestTarget))
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.Bytes()
	require.Len(t, body, maxRouterLogResponse)
	require.NotEqual(t, byte('\n'), body[len(body)-1])
	var response RouterLogResponse
	require.NoError(t, json.Unmarshal(body, &response))
	require.Equal(t, "complete", response.Status)
	require.Empty(t, response.Diagnostics)
	require.Len(t, response.Lines, 2)
	require.Equal(t, 5_591_924, len(response.Lines[0].BytesBase64))
	require.Equal(t, 44_739_240, len(response.Lines[1].BytesBase64))
	require.Equal(t, middle, decodeLineBytes(t, response.Lines[0]))
	require.Equal(t, active, decodeLineBytes(t, response.Lines[1]))
	require.NotNil(t, response.NextCursor)
	require.Len(t, *response.NextCursor, 379)
	require.Equal(t, 484, len(body)-len(response.Lines[0].BytesBase64)-len(response.Lines[1].BytesBase64))
	marshaled, err := json.Marshal(response)
	require.NoError(t, err)
	require.Equal(t, body, marshaled)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))

	candidate := RouterLogResponse{Status: "complete", Lines: []RouterLogLine{
		{BytesBase64: base64.StdEncoding.EncodeToString(old)},
		{BytesBase64: base64.StdEncoding.EncodeToString(middle)},
		{BytesBase64: base64.StdEncoding.EncodeToString(active)},
	}, NextCursor: nil, Diagnostics: []RouterLogDiagnostic{}}
	candidateBody, err := json.Marshal(candidate)
	require.NoError(t, err)
	require.Len(t, candidateBody, 50_331_691)
	require.Equal(t, maxRouterLogResponse+43, len(candidateBody))

	continuationTarget := requestTarget + "&cursor=" + url.QueryEscape(*response.NextCursor)
	continuedRecorder := httptest.NewRecorder()
	router.handleHTTP(continuedRecorder, localRouterLogRequest(http.MethodGet, continuationTarget))
	require.Equal(t, http.StatusOK, continuedRecorder.Code)
	var continued RouterLogResponse
	require.NoError(t, json.Unmarshal(continuedRecorder.Body.Bytes(), &continued))
	require.Equal(t, "complete", continued.Status)
	require.Empty(t, continued.Diagnostics)
	require.Equal(t, []RouterLogLine{{BytesBase64: base64.StdEncoding.EncodeToString(old)}}, continued.Lines)
	require.Nil(t, continued.NextCursor)
	continuedMarshaled, err := json.Marshal(continued)
	require.NoError(t, err)
	require.Equal(t, continuedMarshaled, continuedRecorder.Body.Bytes())
}

func TestRouterLogEndpointReturnsClosedResponseAndPreservesRawLines(t *testing.T) {
	raw := []byte("first\r\n\xff\r\n\nlast")
	router := &Router{ids: randomIDs{}, routerLogReader: RawRouterLogReader(raw)}
	recorder := httptest.NewRecorder()
	router.handleHTTP(recorder, localRouterLogRequest(http.MethodGet, "/v1/router-log?query="))
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
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	response, err := r.ReadRouterLog(context.Background(), RouterLogQuery{Query: "x"})
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	require.NoError(t, err)
	require.Len(t, response.Lines, 500)
	require.Equal(t, 500, base64Encodes)
	require.Equal(t, 500, lineMarshals)
	require.Equal(t, 500, envelopeMarshals)
	require.Equal(t, 499, cursorEncodes)
	require.LessOrEqual(t, cursorEncodes, maxRouterLogMatches)
	if allocationBudgetEnabled {
		require.LessOrEqual(t, allocated, uint64(384<<20), "500x70KiB request allocated %d bytes", allocated)
	}
	t.Logf("500x70KiB counters: base64=%d line_json=%d envelope=%d cursor=%d allocated=%d bytes", base64Encodes, lineMarshals, envelopeMarshals, cursorEncodes, allocated)
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

func TestConfiguredRouterLogScopedPaginationAndCursorBinding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "router.jsonl")
	makeLine := func(agentID string) []byte {
		prefix := []byte(`{"agent_id":"` + agentID + `","message":"`)
		suffix := []byte(`"}`)
		line := append(append([]byte{}, prefix...), bytes.Repeat([]byte{'x'}, 24<<20-len(prefix)-len(suffix))...)
		return append(line, suffix...)
	}
	rotatedLine := makeLine("agent-a")
	activeLine := makeLine("agent-a")
	require.NoError(t, os.WriteFile(path+".20260101T000000.000000000Z", append(append([]byte{}, rotatedLine...), '\n'), 0o600))
	require.NoError(t, os.WriteFile(path, activeLine, 0o600))
	rawReader, err := NewConfiguredRouterLogReader(path)
	require.NoError(t, err)
	reader := rawReader.(*configuredRouterLogReader)
	scope := "agent-a"
	first, err := reader.ReadRouterLog(context.Background(), RouterLogQuery{AgentID: scope})
	require.NoError(t, err)
	require.Len(t, first.Lines, 1)
	require.NotNil(t, first.NextCursor)
	require.Equal(t, activeLine, decodeLineBytes(t, first.Lines[0]))
	continued, err := reader.ReadRouterLog(context.Background(), RouterLogQuery{AgentID: scope, Cursor: first.NextCursor})
	require.NoError(t, err)
	require.Equal(t, []RouterLogLine{{BytesBase64: base64.StdEncoding.EncodeToString(rotatedLine)}}, continued.Lines)
	_, err = reader.ReadRouterLog(context.Background(), RouterLogQuery{AgentID: "agent-b", Cursor: first.NextCursor})
	require.ErrorIs(t, err, errRouterLogInvalidCursor)
}

func localRouterLogRequest(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "127.0.0.1:43210"
	return req
}

func TestManagedRouterLogScopesByDecodedAgentIDBeforeQuery(t *testing.T) {
	raw := []byte("{\"agent_id\":\"agent-a\",\"message\":\"safe\"}\r\n" +
		"{\"agent_id\":\"agent-b\",\"message\":\"agent-a\"}\n" +
		"{\"message\":\"agent-a\"}\n" +
		"malformed agent-a\n" +
		"{\"agent_id\":12,\"message\":\"agent-a\"}\n")
	router := &Router{ids: randomIDs{}, routerLogReader: RawRouterLogReader(raw)}
	recorder := httptest.NewRecorder()
	router.handleHTTP(recorder, localRouterLogRequest(http.MethodGet, "/v1/managed-agents/agent-a/router-log?query=agent-a"))
	require.Equal(t, http.StatusOK, recorder.Code)
	var response RouterLogResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, []RouterLogLine{{BytesBase64: base64.StdEncoding.EncodeToString([]byte("{\"agent_id\":\"agent-a\",\"message\":\"safe\"}\r"))}}, response.Lines)
}

func TestManagedRouterLogRejectsInvalidIDsMethodsAndSpoofedRemoteAddress(t *testing.T) {
	router := &Router{ids: randomIDs{}, routerLogReader: RawRouterLogReader([]byte(`{"agent_id":"agent-a"}`))}
	cases := []struct {
		name, method, target, remote string
		status                       int
	}{
		{"invalid ID", http.MethodGet, "/v1/managed-agents/bad%2Fagent/router-log?query=", "127.0.0.1:1", http.StatusBadRequest},
		{"method", http.MethodPost, "/v1/managed-agents/agent-a/router-log?query=", "127.0.0.1:1", http.StatusMethodNotAllowed},
		{"non-loopback", http.MethodGet, "/v1/managed-agents/agent-a/router-log?query=", "192.0.2.1:1", http.StatusForbidden},
		{"forwarded spoof", http.MethodGet, "/v1/managed-agents/agent-a/router-log?query=", "192.0.2.1:1", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			req.RemoteAddr = tc.remote
			if tc.name == "forwarded spoof" {
				req.Header.Set("X-Forwarded-For", "127.0.0.1")
			}
			recorder := httptest.NewRecorder()
			router.handleHTTP(recorder, req)
			require.Equal(t, tc.status, recorder.Code)
			if tc.status == http.StatusForbidden {
				require.Contains(t, recorder.Body.String(), `"code":"forbidden"`)
				require.NotContains(t, recorder.Body.String(), "192.0.2.1")
			}
		})
	}

	req := localRouterLogRequest(http.MethodGet, "/v1/managed-agents/agent-a/router-log?query=")
	req.Header.Set("Origin", "https://arbitrary.example")
	recorder := httptest.NewRecorder()
	router.handleHTTP(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "*", recorder.Header().Get("Access-Control-Allow-Origin"))
}

func TestLegacyRouterLogUsesTheSameLoopbackBoundary(t *testing.T) {
	router := &Router{ids: randomIDs{}, routerLogReader: RawRouterLogReader([]byte("legacy"))}
	req := httptest.NewRequest(http.MethodGet, "/v1/router-log?query=", nil)
	req.RemoteAddr = "198.51.100.4:9000"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	recorder := httptest.NewRecorder()
	router.handleHTTP(recorder, req)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"code":"forbidden"`)
}

func TestLoopbackRemoteAddrAcceptsServerAndTestForms(t *testing.T) {
	for _, remoteAddr := range []string{"127.0.0.1", "127.0.0.1:43210", "[127.0.0.1]:43210", "::1", "[::1]", "[::1]:43210", "[::1%lo0]:43210", "[::ffff:127.0.0.1]:43210"} {
		require.True(t, loopbackRemoteAddr(remoteAddr), remoteAddr)
	}
	for _, remoteAddr := range []string{"", "localhost:43210", "192.0.2.1:43210", "[2001:db8::1]:43210", "::1:43210", "[127.0.0.1", "127.0.0.1]"} {
		require.False(t, loopbackRemoteAddr(remoteAddr), remoteAddr)
	}
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
