package transport

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// staticUI is a fixed built-asset boundary. It deliberately has no user,
// origin, cookie, or token state: choosing to expose the router is separate
// from serving an application login page.
type staticUI struct {
	root  string
	index string
}

func newStaticUI(root string) (*staticUI, error) {
	if root == "" {
		return nil, nil
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, errors.New("static UI root is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("static UI root must be a directory without symlinks")
	}
	// The directory is an application boundary, not a general file server.
	// Reject symlinks up front and again per request so neither a packaged asset
	// nor a later replacement can turn an in-root URL into an arbitrary read.
	if err := filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("static UI tree must not contain symlinks")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	index := filepath.Join(absolute, "index.html")
	indexInfo, err := os.Lstat(index)
	if err != nil || indexInfo.Mode()&os.ModeSymlink != 0 || !indexInfo.Mode().IsRegular() {
		return nil, errors.New("static UI root must contain index.html")
	}
	return &staticUI{root: absolute, index: index}, nil
}

func staticUIHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data: https://robohash.org; style-src 'self' https://cdnjs.cloudflare.com; font-src 'self' https://cdnjs.cloudflare.com; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
}

// staticUIFile resolves only regular, non-symlinked descendants of root.
// Lstat every path component because checking the final path alone follows a
// symlinked directory before it can reveal the escape.
func (ui *staticUI) staticUIFile(relative string) (string, bool) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", false
	}
	candidate := ui.root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
		candidate = filepath.Join(candidate, part)
		info, err := os.Lstat(candidate)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
	}
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return candidate, true
}

func (r *Router) serveStaticUI(w http.ResponseWriter, req *http.Request) bool {
	ui := r.staticUI
	if ui == nil || req.Method != http.MethodGet && req.Method != http.MethodHead || strings.HasPrefix(req.URL.Path, "/v1/") {
		return false
	}
	staticUIHeaders(w.Header())
	requestPath := strings.TrimPrefix(filepath.Clean("/"+req.URL.Path), "/")
	if requestPath != "" {
		if file, ok := ui.staticUIFile(requestPath); ok {
			if contentType := mime.TypeByExtension(filepath.Ext(file)); contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			http.ServeFile(w, req, file)
			return true
		}
	}
	// A built React application owns client-side navigation. Unknown non-API
	// paths receive its immutable index, while API misses still reach the normal
	// router error handler above. Revalidate index on every serve for the same
	// containment rule used for other files.
	if index, ok := ui.staticUIFile("index.html"); ok {
		http.ServeFile(w, req, index)
		return true
	}
	http.NotFound(w, req)
	return true
}
