//go:build embedded_private_codex_observer

package managed

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Disposable diagnostic only. Never include request or response material in a
// record. The output directory is prepared by the caller, not made by Router.
const privateCodexObserverDir = "MECHA_EMBEDDED_PRIVATE_CODEX_OBSERVER_DIR"

type privateCodexRecord struct {
	Ordinal      int    `json:"ordinal"`
	Status       int    `json:"status"`
	Category     string `json:"category"`
	CodeCategory string `json:"code_category"`
}

type privateCodexStartupRecord struct {
	Stage    string `json:"stage"`
	Outcome  string `json:"outcome"`
	Category string `json:"category"`
}

var privateCodexState struct {
	sync.Mutex
	dirFD    int
	prepared bool
	attempts int // exactly one terminal Do per prepared process
}

// Open every path component relative to a verified descriptor. In particular,
// Lstat followed by Open would leave a symlink-swap window.
func privateCodexOpenDir(path string) (int, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return -1, errors.New("invalid observer directory")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return -1, errors.New("invalid observer path component")
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if e != nil {
			return -1, e
		}
		fd = next
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Geteuid()) || st.Mode&0777 != 0700 {
		unix.Close(fd)
		return -1, errors.New("observer directory must be owner 0700")
	}
	return fd, nil
}

func privateCodexWrite[T privateCodexRecord | privateCodexStartupRecord](fd int, name string, r T) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	out, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer unix.Close(out)
	var st unix.Stat_t
	if err := unix.Fstat(out, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Mode&0777 != 0600 {
		return errors.New("invalid observer output file")
	}
	for len(data) > 0 {
		n, err := unix.Write(out, data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	if err := unix.Fsync(out); err != nil {
		return err
	}
	// Persist the newly created name as well as the fixed-size record before Do.
	return unix.Fsync(fd)
}

// Called before credential loading and before Router spawn. The initial record
// means no instrumented Do has been entered YET, not that preflight succeeded.
func PreparePrivateCodexObserver() error {
	privateCodexState.Lock()
	defer privateCodexState.Unlock()
	if privateCodexState.prepared {
		return errors.New("observer already prepared")
	}
	fd, err := privateCodexOpenDir(os.Getenv(privateCodexObserverDir))
	if err != nil {
		return err
	}
	if err := privateCodexWrite(fd, "start.json", privateCodexRecord{Category: "no_http_do", CodeCategory: "no_code"}); err != nil {
		unix.Close(fd)
		return err
	}
	privateCodexState.dirFD = fd
	privateCodexState.prepared = true
	return nil
}

// WritePrivateStartupStage uses the already prepared private descriptor. The
// caller supplies only compiled stage/outcome/category literals, never errors.
func WritePrivateStartupStage(stage, outcome, category string) error {
	switch stage {
	case "cli", "credential", "config", "store_manifest", "helper", "listen", "root_admission":
	default:
		return errors.New("invalid startup stage")
	}
	if outcome != "entered" && outcome != "result" ||
		(outcome == "entered" && category != "entered") ||
		(outcome == "result" && category != "success" && category != "failed" && category != "snapshot_unavailable_continued") ||
		(category == "snapshot_unavailable_continued" && stage != "helper") {
		return errors.New("invalid startup outcome")
	}
	privateCodexState.Lock()
	defer privateCodexState.Unlock()
	if !privateCodexState.prepared {
		return errors.New("observer not prepared")
	}
	return privateCodexWrite(privateCodexState.dirFD, "startup-"+stage+"-"+outcome+".json", privateCodexStartupRecord{stage, outcome, category})
}

func privateCodexObserverEnter() (int, error) {
	privateCodexState.Lock()
	defer privateCodexState.Unlock()
	if !privateCodexState.prepared || privateCodexState.attempts != 0 {
		return 0, errors.New("observer attempt unavailable")
	}
	if err := privateCodexWrite(privateCodexState.dirFD, "attempt-1-entered.json", privateCodexRecord{Ordinal: 1, Category: "http_do_entered", CodeCategory: "no_code"}); err != nil {
		return 0, err
	}
	privateCodexState.attempts = 1
	return 1, nil
}

func privateCodexObserverResult(n int, resp *http.Response, doErr error) (*http.Response, error) {
	privateCodexState.Lock()
	defer privateCodexState.Unlock()
	if !privateCodexState.prepared || n != 1 || privateCodexState.attempts != 1 {
		return resp, errors.New("observer result unavailable")
	}
	r := privateCodexRecord{Ordinal: n, Category: "transport_error", CodeCategory: "no_code"}
	if doErr == nil && resp != nil {
		r.Status = resp.StatusCode
		if r.Status < 100 || r.Status > 599 {
			r.Status = 0
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			r.Category = "http_2xx"
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			r.Category = "terminal_3xx"
		case resp.StatusCode == 401:
			r.Category = "typed401"
		case resp.StatusCode >= 400 && resp.StatusCode < 500:
			r.Category = "other_4xx"
		case resp.StatusCode >= 500 && resp.StatusCode < 600:
			r.Category = "http_5xx"
		default:
			r.Category = "other_status"
		}
		if resp.StatusCode >= 400 && resp.Body != nil {
			// Never inspect redirects or a successful SSE stream. Every byte
			// read here is put back in front of the original remaining body.
			if mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil && mediaType == "application/json" {
				consumed, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
				remaining := io.Reader(resp.Body)
				if readErr != nil {
					remaining = privateCodexReadError{readErr}
				}
				resp.Body = &privateCodexReplayBody{Reader: io.MultiReader(bytes.NewReader(consumed), remaining), Closer: resp.Body}
				if readErr == nil && len(consumed) <= 4096 {
					r.CodeCategory = privateCodexCode(consumed)
				}
			}
		}
	}
	return resp, privateCodexWrite(privateCodexState.dirFD, "attempt-1-result.json", r)
}

type privateCodexReplayBody struct {
	io.Reader
	io.Closer
}

type privateCodexReadError struct{ err error }

func (r privateCodexReadError) Read([]byte) (int, error) { return 0, r.err }

// Parse bounded JSON tokens, rejecting ambiguous keys before classification.
// Only locally compiled category literals can leave this function.
func privateCodexCode(body []byte) string {
	if len(body) > 4096 {
		return "unknown"
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	tokens := 0
	next := func() (json.Token, bool) {
		if tokens >= 128 {
			return nil, false
		}
		token, err := dec.Token()
		if err != nil {
			return nil, false
		}
		tokens++
		return token, true
	}

	var code string
	codeIsString := false
	var value func(json.Token, int, string) bool
	value = func(token json.Token, depth int, field string) bool {
		if delim, ok := token.(json.Delim); ok {
			if depth > 4 {
				return false
			}
			switch delim {
			case '{':
				seen := make(map[string]bool)
				for dec.More() {
					keyToken, ok := next()
					if !ok {
						return false
					}
					key, ok := keyToken.(string)
					if !ok || seen[key] || depth == 1 && key != "error" || depth == 2 && len(seen) >= 8 {
						return false
					}
					seen[key] = true
					child, ok := next()
					if !ok || !value(child, depth+1, keyIfRelevant(depth, key)) {
						return false
					}
				}
				closeToken, ok := next()
				return ok && closeToken == json.Delim('}') && (depth != 1 || seen["error"])
			case '[':
				if field == "error" {
					return false
				}
				for dec.More() {
					child, ok := next()
					if !ok || !value(child, depth+1, "") {
						return false
					}
				}
				closeToken, ok := next()
				return ok && closeToken == json.Delim(']')
			default:
				return false
			}
		}
		if field == "error" {
			return false
		}
		if field == "code" {
			code, codeIsString = token.(string)
		}
		return true
	}
	first, ok := next()
	if !ok || first != json.Delim('{') || !value(first, 1, "") {
		return "unknown"
	}
	if _, err := dec.Token(); err != io.EOF {
		return "unknown"
	}
	if !codeIsString {
		return "no_code"
	}
	switch code {
	case "invalid_api_key":
		return "invalid_api_key"
	case "token_expired":
		return "token_expired"
	case "rate_limit_exceeded":
		return "rate_limit_exceeded"
	default:
		return "unknown"
	}
}

// Only the root error envelope and its direct code field have meaning.
func keyIfRelevant(depth int, key string) string {
	if depth == 1 && key == "error" || depth == 2 && key == "code" {
		return key
	}
	return ""
}
