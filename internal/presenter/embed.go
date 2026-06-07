package presenter

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
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

// notBuiltHTML is the self-contained notice served at GET / when the
// embedded frontend FS is wired but carries no index.html — the
// .gitkeep-only state of a clean checkout before scripts/build.sh has
// run (e.g. `go run ./cmd/ai-viewer-serve` during development). It is
// intentionally dependency-free (inline styles, no asset references) so
// it renders without the bundle that does not exist yet. The /api
// surface stays fully functional in this state. See presenter.md
// §"serveIndex contract" and architecture.md §"Not-built degrade".
const notBuiltHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>ai-viewer — UI not built</title>
<style>
  body { font-family: system-ui, sans-serif; background: #0d1117; color: #e6edf3;
         margin: 0; display: flex; min-height: 100vh; align-items: center;
         justify-content: center; }
  main { max-width: 34rem; padding: 2rem; }
  h1 { font-size: 1.4rem; margin: 0 0 0.75rem; }
  p { line-height: 1.5; margin: 0 0 0.75rem; }
  code { background: #161b22; border: 1px solid #30363d; border-radius: 6px;
         padding: 0.15rem 0.4rem; font-size: 0.95em; }
</style>
</head>
<body>
<main>
<h1>ai-viewer UI not built</h1>
<p>The server is running and the <code>/api</code> endpoints are live, but the
web UI has not been built into this binary yet.</p>
<p>Build it with:</p>
<p><code>scripts/build.sh</code></p>
<p>Then restart <code>ai-viewer-serve</code>. In development you can instead run
<code>scripts/dev.sh</code>, which serves the live UI from the Vite dev server.</p>
</main>
</body>
</html>
`

// serveIndex answers GET / and HEAD /. It has three states (presenter.md
// §"serveIndex contract"):
//
//   - p.frontend == nil: the frontend was never wired (a test/wiring
//     misconfiguration the production binary cannot reach since it always
//     embeds the FS) → 500.
//   - index.html present: serve the built SPA shell → 200, no-cache.
//   - index.html absent (the .gitkeep-only embed of a clean checkout):
//     serve the built-in not-built notice → 200, no-cache, and log once
//     at Info. An unbuilt UI is a recoverable dev-time state, not a fatal
//     error; embeddedFrontend() in the serve binary likewise never fails.
//
// Cache-Control is no-cache in every served case so the browser always
// re-fetches the entry HTML after a redeploy and picks up new hashed asset
// names. HEAD responses carry the same headers as GET but an empty body,
// per RFC 9110 §9.3.2.
func (p *Presenter) serveIndex(w http.ResponseWriter, r *http.Request) {
	if p.frontend == nil {
		writeJSONError(w, r, p.logger, http.StatusInternalServerError,
			CodeInternalError, "frontend assets not wired into presenter", nil)
		return
	}
	data, err := fs.ReadFile(p.frontend, path.Join(frontendRoot, indexFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			p.serveNotBuiltNotice(w, r)
			return
		}
		// A non-ErrNotExist read failure (corrupt embed, I/O error) is a
		// real fault, not the expected unbuilt state: surface it as 500.
		p.logFrontendError(r, "reading index.html from embed", err)
		writeJSONError(w, r, p.logger, http.StatusInternalServerError,
			CodeInternalError, "frontend assets missing from binary", nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(data); err != nil {
		p.logFrontendError(r, "writing index.html", err)
	}
}

// serveNotBuiltNotice writes the static not-built notice (200, no-cache)
// and logs once at Info that the UI is unbuilt. Kept separate from
// serveIndex so the degrade path is a single small function. HEAD gets
// the same headers with an empty body (RFC 9110 §9.3.2).
func (p *Presenter) serveNotBuiltNotice(w http.ResponseWriter, r *http.Request) {
	p.notBuiltLogOnce.Do(func() {
		if p.logger != nil {
			p.logger.LogAttrs(r.Context(), slog.LevelInfo,
				"presenter: frontend not built; serving placeholder notice (run scripts/build.sh)",
				slog.String("path", r.URL.Path),
				slog.String("request_id", requestIDFromContext(r.Context())),
			)
		}
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(notBuiltHTML)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.WriteString(w, notBuiltHTML); err != nil {
		p.logFrontendError(r, "writing not-built notice", err)
	}
}

// publicRootFiles is the set of root-level files Vite copies from
// frontend/public/ into dist/ that the built index.html references at the
// site root. They are served by servePublicFile via an explicit exact mux
// route each so the file is returned with its correct content-type + cache;
// without the exact route the SPA fallback in rootHandler would serve the
// HTML shell in place of e.g. /favicon.svg. Phase 1 ships only favicon.svg;
// add a string here AND a build-output copy when a new root public file is
// introduced (presenter.md §"Root public assets"). A fixed-size array (not a slice)
// signals this registry is compile-time fixed and never appended to at
// runtime; the mux is built from it once in Handler().
var publicRootFiles = [...]string{"favicon.svg"}

const (
	rootPublicCacheControl = "no-cache"
	assetCacheControl      = "public, max-age=31536000, immutable"
)

// servePublicFile serves a single embedded root public file (e.g.
// /favicon.svg). Unlike serveAsset's hashed bundle, these names are stable
// and not content-hashed, so they carry a revalidating no-cache header rather
// than a long immutable one. The path is the request path minus its leading
// slash; it must be a single safe segment with no traversal. Missing files
// return the structured NOT_FOUND envelope (no SPA fallback for these paths).
func (p *Presenter) servePublicFile(w http.ResponseWriter, r *http.Request) {
	if !staticFileMethodAllowed(r.Method) {
		p.writeStaticMethodNotAllowed(w, r)
		return
	}
	if p.frontend == nil {
		p.writePublicFileNotFound(w, r)
		return
	}

	name, ok := publicRootFileName(r.URL.Path)
	if !ok {
		p.writePublicFileNotFound(w, r)
		return
	}

	data, ok := p.readPublicFile(w, r, name)
	if !ok {
		return
	}
	p.writePublicFileBytes(w, r, name, data)
}

func staticFileMethodAllowed(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func publicRootFileName(rawPath string) (string, bool) {
	name := strings.TrimPrefix(rawPath, "/")
	if name == "" || name != path.Base(name) || strings.Contains(name, "..") {
		return "", false
	}
	return name, true
}

func (p *Presenter) readPublicFile(w http.ResponseWriter, r *http.Request, name string) ([]byte, bool) {
	data, err := fs.ReadFile(p.frontend, path.Join(frontendRoot, name))
	if err == nil {
		return data, true
	}
	if errors.Is(err, fs.ErrNotExist) {
		p.writePublicFileNotFound(w, r)
		return nil, false
	}
	p.logFrontendError(r, "reading public file", err)
	writeJSONError(w, r, p.logger, http.StatusInternalServerError,
		CodeInternalError, "failed to read public file", nil)
	return nil, false
}

func (p *Presenter) writePublicFileBytes(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	w.Header().Set("Content-Type", contentTypeForAsset(name))
	w.Header().Set("Cache-Control", rootPublicCacheControl)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	// The body is trusted embedded build output: servePublicFile is wired
	// ONLY on exact publicRootFiles routes (e.g. /favicon.svg), so `name` is
	// never request-varied, and the bytes are baked into the binary at build
	// time — the response is never request-controlled. gosec's G705 taint
	// analysis cannot see the exact-match mux routing and flags the write.
	if _, err := w.Write(data); err != nil { // #nosec G705 -- trusted embedded build output, not request-controlled
		p.logFrontendError(r, "writing public file", err)
	}
}

func (p *Presenter) writeStaticMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
		CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
}

func (p *Presenter) writePublicFileNotFound(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, r, p.logger, http.StatusNotFound,
		CodeNotFound, "not found", map[string]any{"path": r.URL.Path})
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
	if !staticFileMethodAllowed(r.Method) {
		p.writeStaticMethodNotAllowed(w, r)
		return
	}
	if p.frontend == nil {
		p.writeAssetNotFound(w, r)
		return
	}

	name, f, ok := p.openAssetFile(w, r)
	if !ok {
		return
	}
	defer func() { _ = f.Close() }()

	if !p.assetFileReady(w, r, f) {
		return
	}
	p.writeAssetResponse(w, r, name, f)
}

func (p *Presenter) openAssetFile(w http.ResponseWriter, r *http.Request) (string, fs.File, bool) {
	cleaned, ok := safeAssetPath(r.URL.Path)
	if !ok {
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			CodeBadRequest, "invalid asset path", map[string]any{"path": r.URL.Path})
		return "", nil, false
	}
	full := path.Join(frontendRoot, cleaned)
	f, err := p.frontend.Open(full)
	if err == nil {
		return cleaned, f, true
	}
	if errors.Is(err, fs.ErrNotExist) {
		p.writeAssetNotFound(w, r)
		return "", nil, false
	}
	p.logFrontendError(r, "opening asset", err)
	writeJSONError(w, r, p.logger, http.StatusInternalServerError,
		CodeInternalError, "failed to read asset", nil)
	return "", nil, false
}

func (p *Presenter) assetFileReady(w http.ResponseWriter, r *http.Request, f fs.File) bool {
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		p.writeAssetNotFound(w, r)
		return false
	}
	return true
}

func (p *Presenter) writeAssetResponse(w http.ResponseWriter, r *http.Request, name string, f fs.File) {
	w.Header().Set("Content-Type", contentTypeForAsset(name))
	// Vite emits content-hashed asset filenames so a long cache is safe
	// for /assets/*. The SPA shell itself (index.html) carries
	// no-cache so the operator picks up new hashes on the next reload.
	w.Header().Set("Cache-Control", assetCacheControl)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, f); err != nil {
		p.logFrontendError(r, "streaming asset", err)
	}
}

func (p *Presenter) writeAssetNotFound(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, r, p.logger, http.StatusNotFound,
		CodeNotFound, "asset not found", map[string]any{"path": r.URL.Path})
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
