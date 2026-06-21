// Package extract (continued) — payload file readers shared by the
// ingest path (SOW-0091) and the backfill command. The presenter also
// has a near-identical set of resolvers (internal/presenter/payloads.go)
// but those are tied to the HTTP layer + source-roots security check;
// the ones here are minimal helpers for OFFLINE use (we trust the
// payload_refs we wrote ourselves).
package extract

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// PayloadPreview reads the first `maxBytes` of a payload file referenced
// by a location_uri. Supports the three URI schemes the project emits:
//   - file://<path>            → read up to maxBytes from the file
//   - file://<path>#L<n>       → read line n from the file (1-indexed)
//   - opencode-sqlite://...    → not supported here; return an error so
//     the caller can skip / log instead of crashing a backfill
//
// `compression` is "gzip" or empty; matches payload_refs.compression.
// `maxBytes` caps the returned slice; matches the API preview cap (4 KB).
//
// Returns (data, error). On any read error, returns a descriptive error
// so the caller can log and skip — a single broken payload_ref should
// NEVER abort the whole backfill.
func PayloadPreview(locationURI string, compression string, maxBytes int) ([]byte, error) {
	if strings.HasPrefix(locationURI, "opencode-sqlite://") {
		return nil, fmt.Errorf("opencode-sqlite:// payload reading not supported offline (uri: %s)", locationURI[:minLen(40, len(locationURI))])
	}
	if !strings.HasPrefix(locationURI, "file://") {
		return nil, fmt.Errorf("unsupported payload URI scheme: %s", locationURI[:minLen(40, len(locationURI))])
	}

	pathPart := strings.TrimPrefix(locationURI, "file://")
	if !strings.HasPrefix(pathPart, "/") {
		pathPart = "/" + pathPart
	}

	// Split off #L<line> anchor.
	lineNo := 0
	if idx := strings.Index(pathPart, "#L"); idx >= 0 {
		anchor := pathPart[idx+2:]
		pathPart = pathPart[:idx]
		if n, err := strconv.Atoi(anchor); err == nil && n > 0 {
			lineNo = n
		}
	}

	// Gzip: decompress up to maxBytes.
	if compression == "gzip" {
		return readGzipHead(pathPart, maxBytes)
	}

	// Plain file: read line or head.
	if lineNo > 0 {
		return readFileLine(pathPart, lineNo, maxBytes)
	}
	return readFileHead(pathPart, maxBytes)
}

// readFileHead reads up to maxBytes from the start of the file.
func readFileHead(path string, maxBytes int) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 — path comes from our own payload_refs row
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read: %w", err)
	}
	return buf[:n], nil
}

// readFileLine reads the nth 1-indexed line from the file, capped at
// maxBytes. The line is returned WITHOUT the trailing newline.
func readFileLine(path string, lineNo int, maxBytes int) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	// Allow lines up to maxBytes (default bufio.Scanner buffer is 64 KB).
	scanner.Buffer(make([]byte, 0, 64*1024), maxBytes)
	current := 0
	for scanner.Scan() {
		current++
		if current == lineNo {
			line := scanner.Bytes()
			if len(line) > maxBytes {
				line = line[:maxBytes]
			}
			// Return a copy because the scanner reuses its buffer.
			out := make([]byte, len(line))
			copy(out, line)
			return out, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return nil, fmt.Errorf("line %d not found in %s", lineNo, path)
}

// readGzipHead decompresses up to maxBytes from a gzipped file.
func readGzipHead(path string, maxBytes int) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(gz, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read gzip: %w", err)
	}
	return buf[:n], nil
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ReadableTextFromRef is a convenience wrapper: open the payload, extract
// the readable text. Returns ("", nil) on any read error so the caller
// can log + skip without aborting the batch. Use this in the backfill
// path; the incremental path uses the same extract.ReadableText directly
// on the bytes it has in memory.
func ReadableTextFromRef(locationURI string, compression string, maxBytes int) string {
	data, err := PayloadPreview(locationURI, compression, maxBytes)
	if err != nil {
		// Swallow the error: we don't want one broken payload_ref to abort
		// a 1.7M-row backfill. The caller can count skipped refs separately
		// if it cares; we just return "" so fts_content stays empty for
		// that op and the search index reflects "no readable text".
		return ""
	}
	_ = bytes.TrimSpace // keep import alive in case future helper needs it
	return ReadableText(data)
}
