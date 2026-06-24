package parity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // register SQLite driver for read-only payload resolution
)

const opencodeSQLiteScheme = "opencode-sqlite"

func opencodePayloadSelector(partID string, field string) Selector {
	return Selector{
		URI:       opencodeSQLiteScheme + "://?part_id=" + url.QueryEscape(partID) + "&field=" + url.QueryEscape(field),
		FieldPath: field,
	}
}

func opencodePayloadNativeID(partID string, field string) string {
	return "part:" + partID + ":" + field
}

func opencodeInputPayloadSelector(inputID string, field string) Selector {
	return Selector{
		URI:       opencodeSQLiteScheme + "://?input_id=" + url.QueryEscape(inputID) + "&field=" + url.QueryEscape(field),
		FieldPath: field,
	}
}

func opencodeInputPayloadNativeID(inputID string, field string) string {
	return "input:" + inputID + ":" + field
}

func resolveOpencodeSQLitePayload(sourceLocation string, parsed *url.URL) (resolvedPayload, error) {
	dbPath := sourceLocationPath(sourceLocation)
	if dbPath == "" {
		return resolvedPayload{}, fmt.Errorf("opencode payload resolver needs absolute source database path")
	}
	partID := parsed.Query().Get("part_id")
	inputID := parsed.Query().Get("input_id")
	field := parsed.Query().Get("field")
	if field == "" {
		return resolvedPayload{}, fmt.Errorf("opencode payload selector requires field")
	}
	if (partID == "") == (inputID == "") {
		return resolvedPayload{}, fmt.Errorf("opencode payload selector requires exactly one of part_id or input_id")
	}

	db, err := openOpencodeParityReadOnly(dbPath)
	if err != nil {
		return resolvedPayload{}, err
	}
	defer func() { _ = db.Close() }()

	if inputID != "" {
		var raw string
		if err := db.QueryRowContext(context.Background(), `SELECT prompt FROM session_input WHERE id = ?`, inputID).Scan(&raw); err != nil {
			return resolvedPayload{}, fmt.Errorf("read opencode session_input %q: %w", inputID, err)
		}
		return resolveOpencodeInputPayloadField([]byte(raw), field)
	}

	var raw string
	if err := db.QueryRowContext(context.Background(), `SELECT data FROM part WHERE id = ?`, partID).Scan(&raw); err != nil {
		return resolvedPayload{}, fmt.Errorf("read opencode part %q: %w", partID, err)
	}
	return resolveOpencodePayloadField([]byte(raw), field)
}

func openOpencodeParityReadOnly(dbPath string) (*sql.DB, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("opencode source database path is empty")
	}
	target := dbPath
	if !strings.HasPrefix(target, "file:") && target != ":memory:" {
		abs, err := filepath.Abs(target)
		if err != nil {
			return nil, fmt.Errorf("resolve opencode source database path: %w", err)
		}
		target = (&url.URL{Scheme: "file", Path: abs}).String()
	}
	values := url.Values{}
	values.Set("mode", "ro")
	values.Set("_txlock", "deferred")
	values.Add("_pragma", "query_only(true)")
	values.Add("_pragma", "busy_timeout(5000)")
	separator := "?"
	if strings.Contains(target, "?") {
		separator = "&"
	}
	db, err := sql.Open("sqlite", target+separator+values.Encode())
	if err != nil {
		return nil, fmt.Errorf("open opencode source database read-only: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping opencode source database read-only: %w", err)
	}
	return db, nil
}

func resolveOpencodePayloadField(raw []byte, field string) (resolvedPayload, error) {
	var doc interface{}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return resolvedPayload{}, fmt.Errorf("decode opencode part data: %w", err)
	}
	value, err := opencodeFieldPathValue(doc, field)
	if err != nil {
		return resolvedPayload{}, err
	}
	if value == nil {
		return resolvedPayload{}, fmt.Errorf("opencode field %q is null", field)
	}
	if text, ok := value.(string); ok {
		return resolvedPayload{bytes: []byte(text), hashDomain: HashSemanticText}, nil
	}
	canonical, err := canonicalIdentityBytes(value)
	if err != nil {
		return resolvedPayload{}, err
	}
	return resolvedPayload{bytes: canonical, hashDomain: HashCanonicalJSON}, nil
}

func resolveOpencodeInputPayloadField(raw []byte, field string) (resolvedPayload, error) {
	const prefix = "prompt."
	if !strings.HasPrefix(field, prefix) {
		return resolvedPayload{}, fmt.Errorf("opencode session_input field %q must start with %q", field, prefix)
	}
	return resolveOpencodePayloadField(raw, strings.TrimPrefix(field, prefix))
}

func opencodeFieldPathValue(doc interface{}, field string) (interface{}, error) {
	if field == "" {
		return nil, fmt.Errorf("opencode field path is empty")
	}
	current := doc
	for _, token := range strings.Split(field, ".") {
		switch value := current.(type) {
		case map[string]interface{}:
			next, ok := value[token]
			if !ok {
				return nil, fmt.Errorf("opencode field %q missing token %q", field, token)
			}
			current = next
		case []interface{}:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(value) {
				return nil, fmt.Errorf("opencode field %q invalid array index %q", field, token)
			}
			current = value[index]
		default:
			return nil, fmt.Errorf("opencode field %q cannot descend into %T", field, current)
		}
	}
	return current, nil
}
