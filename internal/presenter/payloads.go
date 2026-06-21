package presenter

import (
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// payloadPreviewBytes is the default preview size (4 KB). The full download is
// capped at payloadMaxBytes. payloadJSONCapBytes is the cap for JSON-format
// payloads (codex/claude_code/opencode envelopes), which the TurnView needs
// whole for extractReadableText to strip the envelope.
const (
	payloadPreviewBytes = 4096
	payloadJSONCapBytes = 1 * 1024 * 1024  // 1 MB — most JSON envelopes fit
	payloadMaxBytes     = 10 * 1024 * 1024 // 10 MB
)

// payloadRefRow is the DB row for a payload_refs entry.
type payloadRefRow struct {
	ID            int64
	LocationURI   string
	Format        string
	Compression   sql.NullString
	OriginalBytes sql.NullInt64
}

// handlePayloadPreview answers GET /api/payloads/:id — returns the first ~4 KB
// of the referenced payload as text, or the full payload (capped at 10 MB) with
// ?full=1. The :id is the payload_refs.id (integer).
//
// SECURITY: the resolved file path MUST be under one of the configured source
// roots (from the sources table). A path outside any source root is rejected
// with 403. This prevents path-traversal / arbitrary file read via a crafted
// location_uri (SOW-0033 AC#2).
func (p *Presenter) handlePayloadPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, r, p.logger, http.StatusMethodNotAllowed,
			CodeMethodNotAllowed, "method not allowed", map[string]any{"method": r.Method})
		return
	}

	// Extract :id from the path (the router registered /api/payloads/).
	idStr := strings.TrimPrefix(r.URL.Path, "/api/payloads/")
	idStr = strings.Trim(idStr, "/")
	if idStr == "" {
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			CodeBadRequest, "payload id required", nil)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, r, p.logger, http.StatusBadRequest,
			CodeBadRequest, "invalid payload id", nil)
		return
	}

	// Look up the payload ref.
	ref, err := p.lookupPayloadRef(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, r, p.logger, http.StatusNotFound,
				CodeNotFound, "payload not found", nil)
			return
		}
		p.logger.LogAttrs(r.Context(), slog.LevelError, "presenter: payload lookup failed",
			slog.Any("err", err), slog.Int64("payload_id", id))
		writeJSONError(w, r, p.logger, http.StatusInternalServerError,
			CodeInternalError, "failed to look up payload", nil)
		return
	}

	// Resolve the source roots for path containment.
	roots, err := p.sourceRoots(r.Context())
	if err != nil {
		p.logger.LogAttrs(r.Context(), slog.LevelError, "presenter: source roots lookup failed",
			slog.Any("err", err))
		writeJSONError(w, r, p.logger, http.StatusInternalServerError,
			CodeInternalError, "failed to resolve source roots", nil)
		return
	}

	// Determine the max bytes to return.
	// Determine the max bytes to return. JSON envelopes (codex
	// response_item.message, claude_code assistant message, opencode
	// parts[]) are typically 5–50 KB and ALWAYS parseable as JSON when
	// delivered whole. The 4 KB preview cap is fine for the SpanDetailDrawer
	// (truncated prose is still readable) but for the TurnView the
	// extractReadableText heuristic (SOW-0090 chunk 7+) requires a
	// complete JSON document to strip the envelope. We raise the cap to
	// 1 MB for JSON formats so the envelope almost always fits; non-JSON
	// payloads (e.g. plain-text bash output) keep the small preview cap.
	maxBytes := payloadPreviewBytes
	if ref.Format == "json" {
		maxBytes = payloadJSONCapBytes
	}
	full := r.URL.Query().Has("full")
	if full {
		maxBytes = payloadMaxBytes
	}

	// Resolve the payload to bytes.
	data, truncated, totalBytes, err := p.resolvePayload(r.Context(), ref, roots, maxBytes)
	// JSON-aware truncation: when we cut the file at maxBytes, the result
	// often lands mid-string. truncateToJSONBoundary drops trailing bytes
	// after the closing brace/bracket that completes the top-level JSON
	// document. The X-Payload-Truncated flag stays true (we're still
	// showing less than the full payload); only the byte count changes.
	if truncated && ref.Format == "json" {
		data = truncateToJSONBoundary(data)
	}
	if err != nil {
		status := http.StatusInternalServerError
		code := CodeInternalError
		msg := "failed to read payload"
		if strings.Contains(err.Error(), "outside source roots") {
			status = http.StatusForbidden
			code = CodeBadRequest
			msg = "payload path is outside configured source roots"
		} else if strings.Contains(err.Error(), "file not found") {
			status = http.StatusNotFound
			code = CodeNotFound
			msg = "payload file not found"
		}
		p.logger.LogAttrs(r.Context(), slog.LevelWarn, "presenter: payload resolve failed",
			slog.Any("err", err), slog.Int64("payload_id", id))
		writeJSONError(w, r, p.logger, status, code, msg, nil)
		return
	}

	// Set response headers.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Payload-Format", ref.Format)
	w.Header().Set("X-Payload-Truncated", strconv.FormatBool(truncated))
	w.Header().Set("X-Payload-Total-Bytes", strconv.FormatInt(totalBytes, 10))
	if !full {
		w.Header().Set("X-Payload-Preview-Bytes", strconv.Itoa(len(data)))
	}

	// HEAD: just headers.
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// lookupPayloadRef fetches one payload_refs row by ID.
func (p *Presenter) lookupPayloadRef(ctx context.Context, id int64) (payloadRefRow, error) {
	var row payloadRefRow
	err := p.db.QueryRowContext(ctx, `
SELECT id, location_uri, format, compression, original_bytes
FROM payload_refs WHERE id = ?`, id).Scan(
		&row.ID, &row.LocationURI, &row.Format, &row.Compression, &row.OriginalBytes)
	return row, err
}

// sourceRoots returns the canonicalized absolute paths of every source's
// location — the containment set for payload file reads.
func (p *Presenter) sourceRoots(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT DISTINCT location FROM sources`)
	if err != nil {
		return nil, fmt.Errorf("query source locations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var roots []string
	for rows.Next() {
		var loc string
		if err := rows.Scan(&loc); err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(loc)
		if err != nil {
			continue
		}
		roots = append(roots, filepath.Clean(abs))
	}
	return roots, rows.Err()
}

// isUnderRoot checks whether resolvedPath is under at least one of roots.
func isUnderRoot(resolvedPath string, roots []string) bool {
	clean := filepath.Clean(resolvedPath)
	for _, root := range roots {
		if clean == root {
			return true
		}
		rel, err := filepath.Rel(root, clean)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// resolvePayload resolves a payload_ref to bytes, handling the three URI
// schemes (file://, file://...#L<line>, opencode-sqlite://). Returns the data
// (bounded to maxBytes), whether it was truncated, and the total payload size.
func (p *Presenter) resolvePayload(ctx context.Context, ref payloadRefRow, roots []string, maxBytes int) ([]byte, bool, int64, error) {
	uri := ref.LocationURI
	switch {
	case strings.HasPrefix(uri, "opencode-sqlite://"):
		return p.resolveOpencodePayload(ctx, uri, maxBytes)
	case strings.HasPrefix(uri, "file://"):
		return p.resolveFilePayload(uri, ref.Compression, roots, maxBytes)
	default:
		return nil, false, 0, fmt.Errorf("unsupported payload URI scheme: %s", uri[:minLen(len(uri), 40)])
	}
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// resolveFilePayload reads a file:// payload, optionally gzip-decompressing and
// optionally extracting a specific JSONL line (#L<line>).
func (p *Presenter) resolveFilePayload(uri string, compression sql.NullString, roots []string, maxBytes int) ([]byte, bool, int64, error) {
	// Strip file:// prefix.
	pathPart := strings.TrimPrefix(uri, "file://")
	// On Linux the path starts with / so file:///path → pathPart = /path (correct).
	// Some URIs may have file:// without the triple slash (file://path).
	if !strings.HasPrefix(pathPart, "/") {
		pathPart = "/" + pathPart
	}

	// Split off #L<line> anchor if present.
	lineNo := 0
	if idx := strings.Index(pathPart, "#L"); idx >= 0 {
		anchor := pathPart[idx+2:]
		pathPart = pathPart[:idx]
		if n, err := strconv.Atoi(anchor); err == nil && n > 0 {
			lineNo = n
		}
	}

	// Canonicalize + security check.
	absPath, err := filepath.Abs(filepath.Clean(pathPart))
	if err != nil {
		return nil, false, 0, fmt.Errorf("resolve path: %w", err)
	}
	if !isUnderRoot(absPath, roots) {
		return nil, false, 0, fmt.Errorf("payload path %s is outside source roots", absPath)
	}

	// Check file exists.
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, false, 0, fmt.Errorf("file not found: %w", err)
	}

	// For #L<line> payloads (codex JSONL): read the specific line.
	if lineNo > 0 {
		return p.readFileLine(absPath, lineNo, maxBytes)
	}

	// For gzip payloads: decompress and return preview/full.
	if compression.Valid && compression.String == "gzip" {
		return readGzipPayload(absPath, info.Size(), maxBytes)
	}

	// Plain file: read directly.
	return readFilePayload(absPath, info.Size(), maxBytes)
}

// readFileLine reads one line (1-indexed) from a file and returns up to maxBytes.
func (p *Presenter) readFileLine(path string, lineNo int, maxBytes int) ([]byte, bool, int64, error) {
	f, err := os.Open(path) // #nosec G304 — path is already validated under source roots
	if err != nil {
		return nil, false, 0, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Scan to the target line.
	currentLine := 0
	// Read line by line using a simple scanner.
	chunk := make([]byte, 65536)
	lineBuf := make([]byte, 0, 65536)
	for {
		n, readErr := f.Read(chunk)
		if n > 0 {
			for i := 0; i < n; i++ {
				if chunk[i] == '\n' {
					currentLine++
					if currentLine == lineNo {
						// This line's content is in lineBuf.
						total := int64(len(lineBuf))
						preview := lineBuf
						truncated := false
						if len(preview) > maxBytes {
							preview = preview[:maxBytes]
							truncated = true
						}
						return preview, truncated, total, nil
					}
					lineBuf = lineBuf[:0] // reset for next line
				} else {
					lineBuf = append(lineBuf, chunk[i])
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, false, 0, fmt.Errorf("read file: %w", readErr)
		}
	}
	return nil, false, 0, fmt.Errorf("line %d not found (file has %d lines)", lineNo, currentLine)
}

// resolveOpencodePayload reads a payload from the opencode SQLite DB.
// URI format: opencode-sqlite://?part_id=X&field=text
func (p *Presenter) resolveOpencodePayload(ctx context.Context, uri string, maxBytes int) ([]byte, bool, int64, error) {
	// Parse query params.
	queryPart := strings.TrimPrefix(uri, "opencode-sqlite://?")
	var partID, field string
	for _, pair := range strings.Split(queryPart, "&") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "part_id":
			partID = kv[1]
		case "field":
			field = kv[1]
		}
	}
	if partID == "" {
		return nil, false, 0, fmt.Errorf("opencode payload: missing part_id")
	}
	if field == "" {
		field = "text"
	}

	// Find the opencode source DB path.
	var dbPath string
	err := p.db.QueryRowContext(ctx,
		`SELECT location FROM sources WHERE format = 'opencode' LIMIT 1`).Scan(&dbPath)
	if err != nil {
		return nil, false, 0, fmt.Errorf("find opencode source: %w", err)
	}

	// Open the opencode DB read-only and read the part's field.
	ocDB, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return nil, false, 0, fmt.Errorf("open opencode db: %w", err)
	}
	defer func() { _ = ocDB.Close() }()

	// The field is a column in the part table (text, state.output, etc.).
	// state.output is accessed via json_extract.
	var content sql.NullString
	query := fmt.Sprintf(`SELECT "%s" FROM part WHERE id = ?`, field) // #nosec G201 — field is from our own URI, not user input
	if strings.HasPrefix(field, "state.") {
		jsonPath := "$." + strings.TrimPrefix(field, "state.")
		query = fmt.Sprintf(`SELECT json_extract(data, '%s') FROM part WHERE id = ?`, jsonPath) // #nosec G201
	}
	err = ocDB.QueryRowContext(ctx, query, partID).Scan(&content)
	if err != nil {
		return nil, false, 0, fmt.Errorf("query opencode part: %w", err)
	}
	if !content.Valid || content.String == "" {
		return nil, false, 0, fmt.Errorf("opencode part %s field %s is empty", partID, field)
	}

	data := []byte(content.String)
	total := int64(len(data))
	if len(data) > maxBytes {
		return data[:maxBytes], true, total, nil
	}
	return data, false, total, nil
}

// readGzipPayload opens a gzip file, decompresses, and returns up to maxBytes.
func readGzipPayload(path string, fileSize int64, maxBytes int) ([]byte, bool, int64, error) {
	f, err := os.Open(path) // #nosec G304 — path is validated under source roots
	if err != nil {
		return nil, false, 0, fmt.Errorf("open gzip file: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, false, 0, fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	// Read up to maxBytes + 1 (to detect truncation).
	buf := make([]byte, maxBytes+1)
	n, err := io.ReadFull(gz, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, false, 0, fmt.Errorf("gzip read: %w", err)
	}
	truncated := n > maxBytes
	if truncated {
		n = maxBytes
	}
	// We don't know the total decompressed size without reading it all;
	// report the file size as a proxy (good enough for the truncation indicator).
	return buf[:n], truncated, fileSize, nil
}

// readFilePayload reads a plain file and returns up to maxBytes.
func readFilePayload(path string, fileSize int64, maxBytes int) ([]byte, bool, int64, error) {
	f, err := os.Open(path) // #nosec G304 — path is validated under source roots
	if err != nil {
		return nil, false, 0, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	readSize := maxBytes
	if readSize > int(fileSize) {
		readSize = int(fileSize)
	}
	buf := make([]byte, readSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, false, 0, fmt.Errorf("read file: %w", err)
	}
	truncated := fileSize > int64(maxBytes)
	return buf[:n], truncated, fileSize, nil
}

// truncateToJSONBoundary walks the input bytes looking for the position
// after which depth returns to zero AND we have seen at least one
// opening brace or bracket (the document has been completed). It returns
// the input unchanged when no complete document is found — the caller
// still serves the bytes; the client then has whatever partial JSON
// was readable.
//
// Why: server-side 4 KB payload previews often cut a JSON envelope
// mid-string. The turn view's extractReadableText (frontend) tries to
// JSON.parse the response; a truncated document fails and the user sees
// a wall of JSON. By truncating at a clean JSON boundary, we deliver a
// parseable document whenever the cap is large enough to contain one.
//
// The walker is intentionally minimal — it tracks brace/bracket depth,
// respects double-quoted strings (including backslash escapes), and
// ignores single-quoted strings, comments, and trailing commas (JSON
// itself does not have those, so they're not relevant here).
func truncateToJSONBoundary(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	// Quick reject: not starting with { or [ is not a JSON document.
	if data[0] != '{' && data[0] != '[' {
		return data
	}
	depth := 0
	inString := false
	escaped := false
	seenOpen := false
	for i := 0; i < len(data); i++ {
		b := data[i]
		if escaped {
			escaped = false
			continue
		}
		if inString {
			switch b {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
			seenOpen = true
		case '}', ']':
			depth--
			if seenOpen && depth == 0 {
				// Complete document. Return up to and including this byte.
				return data[:i+1]
			}
		}
	}
	// No complete document within the input. Return as-is so the caller
	// still has something to show.
	return data
}
