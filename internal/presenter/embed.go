package presenter

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
)

// frontendRoot is the directory under the injected frontend FS that
// holds the embedded assets. The serve binary embeds
// `cmd/ai-viewer-serve/frontend_dist/` via go:embed and passes the
// resulting fs.FS in via Options.FrontendFS.
const frontendRoot = "frontend_dist"

// indexFile is the SPA entry point served for any non-asset path that
// the router does not handle directly. Per presenter.md §Routing, the
// React app owns client-side routing.
const indexFile = "index.html"

// serveIndex writes the embedded index.html with sensible cache
// headers. Used for GET / and HEAD /. Cache-Control is no-cache so the
// browser always re-fetches the entry HTML after a redeploy. HEAD
// responses carry the same headers as GET but an empty body, per
// RFC 9110 §9.3.2.
func (p *Presenter) serveIndex(w http.ResponseWriter, r *http.Request) {
	if p.frontend == nil {
		writeJSONError(w, r, p.logger, http.StatusInternalServerError,
			CodeInternalError, "frontend assets not wired into presenter", nil)
		return
	}
	data, err := fs.ReadFile(p.frontend, path.Join(frontendRoot, indexFile))
	if err != nil {
		p.logFrontendError(r, "index.html missing from embed", err)
		writeJSONError(w, r, p.logger, http.StatusInternalServerError,
			CodeInternalError, "frontend assets missing from binary", nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(data); err != nil {
		p.logFrontendError(r, "writing index.html", err)
	}
}

// serveAsset serves a single file under /assets/. Falls through to 404
// when the file is missing; the SPA-fallback to index.html is
// deliberately NOT applied for asset paths so a missing bundle surfaces
// as a real failure rather than masquerading as the SPA entry. Per
// presenter.md §"Routing":
//
//	GET  /assets/* → embedded frontend assets
//	HEAD /assets/* → same headers, empty body (RFC 9110 §9.3.2)
//	(no SPA fallback — 404 on miss)
func (p *Presenter) serveAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}
	if p.frontend == nil {
		writeJSONError(w, r, p.logger, http.StatusNotFound,
			CodeNotFound, "asset not found", map[string]any{"path": r.URL.Path})
		return
	}
	cleaned, ok := safeAssetPath(r.URL.Path)
	if !ok {
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			CodeBadRequest, "invalid asset path", map[string]any{"path": r.URL.Path})
		return
	}
	full := path.Join(frontendRoot, cleaned)
	f, err := p.frontend.Open(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeJSONError(w, r, p.logger, http.StatusNotFound,
				CodeNotFound, "asset not found", map[string]any{"path": r.URL.Path})
			return
		}
		p.logFrontendError(r, "opening asset", err)
		writeJSONError(w, r, p.logger, http.StatusInternalServerError,
			CodeInternalError, "failed to read asset", nil)
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		writeJSONError(w, r, p.logger, http.StatusNotFound,
			CodeNotFound, "asset not found", map[string]any{"path": r.URL.Path})
		return
	}

	w.Header().Set("Content-Type", contentTypeForAsset(cleaned))
	// Vite emits content-hashed asset filenames so a long cache is safe
	// for /assets/*. The SPA shell itself (index.html) carries
	// no-cache so the operator picks up new hashes on the next reload.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, f); err != nil {
		p.logFrontendError(r, "streaming asset", err)
	}
}

// safeAssetPath strips the /assets/ prefix and rejects any segment that
// could traverse out of frontend_dist (`..`, leading `/`, etc.). The
// result is a clean relative path safe to join under frontendRoot.
func safeAssetPath(rawPath string) (string, bool) {
	const prefix = "/assets/"
	if !strings.HasPrefix(rawPath, prefix) {
		return "", false
	}
	rest := rawPath[len(prefix):]
	if rest == "" || strings.Contains(rest, "..") {
		return "", false
	}
	cleaned := path.Clean(rest)
	if strings.HasPrefix(cleaned, "/") || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return "", false
	}
	return path.Join("assets", cleaned), true
}

// contentTypeForAsset returns a sensible Content-Type for the asset.
// embed.FS does not expose mime types and Vite's hashed filenames carry
// the original extension, so a simple extension switch covers every
// asset Vite emits in Phase 1. Unknown extensions get
// application/octet-stream so the browser falls back to download.
func contentTypeForAsset(p string) string {
	switch lowerExt(p) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".map":
		return "application/json; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// lowerExt returns the lower-cased file extension (including the dot)
// for p. Empty string when p has no extension.
func lowerExt(p string) string {
	dot := strings.LastIndexByte(p, '.')
	if dot < 0 {
		return ""
	}
	return strings.ToLower(p[dot:])
}

// logFrontendError records a structured frontend-serving failure. Kept
// in one place so future telemetry hooks attach uniformly. request_id
// is sourced from r.Context() so the log line can be correlated with
// the deferred access log + any writeJSONError warning emitted on the
// same request — see observability.md §"Trace IDs".
func (p *Presenter) logFrontendError(r *http.Request, msg string, err error) {
	if p.logger == nil {
		return
	}
	p.logger.LogAttrs(r.Context(), slog.LevelError, "presenter: "+msg,
		slog.String("path", r.URL.Path),
		slog.String("request_id", requestIDFromContext(r.Context())),
		slog.Any("err", err),
	)
}
