// extract.PayloadPreview (SOW-0091) — offline reader for payload files.
// Mirrors the relevant parts of internal/presenter/payloads.go but
// without the source-roots security check (offline use; we trust our
// own payload_refs rows).
package extract

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestPayloadPreview_FileHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.json")
	if err := os.WriteFile(path, []byte(`{"hello": "world"}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	data, err := PayloadPreview("file://"+path, "", 1024)
	if err != nil {
		t.Fatalf("PayloadPreview: %v", err)
	}
	if string(data) != `{"hello": "world"}` {
		t.Errorf("payload: want %q, got %q", `{"hello": "world"}`, string(data))
	}
}

func TestPayloadPreview_FileHead_Capped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.json")
	if err := os.WriteFile(path, []byte(`{"hello": "this is a long payload that exceeds the cap"}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	data, err := PayloadPreview("file://"+path, "", 16)
	if err != nil {
		t.Fatalf("PayloadPreview: %v", err)
	}
	if len(data) != 16 {
		t.Errorf("payload: want 16 bytes, got %d (%q)", len(data), string(data))
	}
}

func TestPayloadPreview_JSONLLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	content := "line1\nline2 is the one we want\nline3\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	data, err := PayloadPreview("file://"+path+"#L2", "", 1024)
	if err != nil {
		t.Fatalf("PayloadPreview: %v", err)
	}
	if string(data) != "line2 is the one we want" {
		t.Errorf("payload: want %q, got %q", "line2 is the one we want", string(data))
	}
}

func TestPayloadPreview_Gzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.json.gz")
	src := []byte(`{"hello": "gzipped world"}`)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(src); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	data, err := PayloadPreview("file://"+path, "gzip", 1024)
	if err != nil {
		t.Fatalf("PayloadPreview: %v", err)
	}
	if string(data) != `{"hello": "gzipped world"}` {
		t.Errorf("payload: want %q, got %q", `{"hello": "gzipped world"}`, string(data))
	}
}

func TestPayloadPreview_OpencodeNotSupported(t *testing.T) {
	_, err := PayloadPreview("opencode-sqlite:///path/to/db#row=123", "", 1024)
	if err == nil {
		t.Fatal("expected error for opencode-sqlite:// URI")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("not supported")) {
		t.Errorf("error should mention 'not supported', got %q", err.Error())
	}
}

func TestPayloadPreview_UnsupportedScheme(t *testing.T) {
	_, err := PayloadPreview("https://example.com/payload", "", 1024)
	if err == nil {
		t.Fatal("expected error for https:// URI")
	}
}

func TestPayloadPreview_NonexistentFile(t *testing.T) {
	_, err := PayloadPreview("file:///nonexistent/path/abc", "", 1024)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestReadableTextFromRef_CodexEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "msg.json")
	body := `{"timestamp":"2026-06-20T21:08:39.295Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>\nFilesystem sandboxing"}]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := ReadableTextFromRef("file://"+path, "", 8192)
	want := "<permissions instructions>\nFilesystem sandboxing"
	if got != want {
		t.Errorf("ReadableTextFromRef: want %q, got %q", want, got)
	}
}

func TestReadableTextFromRef_BrokenRefReturnsEmpty(t *testing.T) {
	// A broken payload_ref should NOT abort a backfill; it should
	// return "" so fts_content stays empty for that op and the
	// search reflects "no readable text".
	got := ReadableTextFromRef("file:///nonexistent/path/abc", "", 1024)
	if got != "" {
		t.Errorf("ReadableTextFromRef on broken ref: want empty string, got %q", got)
	}
}
