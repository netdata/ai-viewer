package parity

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CanonicalQuerier is the read surface required by canonical parity extraction.
// Both *sql.DB and *sql.Tx satisfy it.
type CanonicalQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ExtractCanonical builds a parity manifest from canonical SQLite rows.
func ExtractCanonical(ctx context.Context, db *sql.DB) ([]Artifact, error) {
	if db == nil {
		return nil, fmt.Errorf("extract canonical manifest: nil *sql.DB")
	}

	return extractCanonical(ctx, db, canonicalSourceScope{})
}

// ExtractCanonicalToWriter streams parity artifacts from canonical SQLite rows.
func ExtractCanonicalToWriter(ctx context.Context, db *sql.DB, writer ArtifactWriter) error {
	if db == nil {
		return fmt.Errorf("extract canonical manifest: nil *sql.DB")
	}
	if writer == nil {
		return fmt.Errorf("extract canonical manifest: nil artifact writer")
	}

	return extractCanonicalToWriter(ctx, db, canonicalSourceScope{}, nil, writer)
}

// ExtractCanonicalFromQuerier builds a parity manifest from a canonical query
// surface, usually a pinned read-only transaction.
func ExtractCanonicalFromQuerier(ctx context.Context, querier CanonicalQuerier) ([]Artifact, error) {
	if querier == nil {
		return nil, fmt.Errorf("extract canonical manifest: nil querier")
	}

	return extractCanonical(ctx, querier, canonicalSourceScope{})
}

// ExtractCanonicalFromQuerierToWriter streams parity artifacts from a canonical
// query surface, usually a pinned read-only transaction.
func ExtractCanonicalFromQuerierToWriter(ctx context.Context, querier CanonicalQuerier, writer ArtifactWriter) error {
	if querier == nil {
		return fmt.Errorf("extract canonical manifest: nil querier")
	}
	if writer == nil {
		return fmt.Errorf("extract canonical manifest: nil artifact writer")
	}

	return extractCanonicalToWriter(ctx, querier, canonicalSourceScope{}, nil, writer)
}

// ExtractCanonicalForSourceIDs builds a parity manifest for selected source IDs.
func ExtractCanonicalForSourceIDs(ctx context.Context, db *sql.DB, sourceIDs []string) ([]Artifact, error) {
	if db == nil {
		return nil, fmt.Errorf("extract canonical manifest: nil *sql.DB")
	}
	scope, err := canonicalScopeForSourceIDs(sourceIDs)
	if err != nil {
		return nil, err
	}

	return extractCanonical(ctx, db, scope)
}

// ExtractCanonicalForSourceIDsToWriter streams canonical parity artifacts for
// selected source IDs without materializing the full artifact slice.
func ExtractCanonicalForSourceIDsToWriter(ctx context.Context, db *sql.DB, sourceIDs []string, writer ArtifactWriter) error {
	if db == nil {
		return fmt.Errorf("extract canonical manifest: nil *sql.DB")
	}
	if writer == nil {
		return fmt.Errorf("extract canonical manifest: nil artifact writer")
	}
	scope, err := canonicalScopeForSourceIDs(sourceIDs)
	if err != nil {
		return err
	}

	return extractCanonicalToWriter(ctx, db, scope, nil, writer)
}

// ExtractCanonicalForSourceIDsFromQuerier builds a scoped canonical parity
// manifest from a canonical query surface, usually a pinned read-only
// transaction.
func ExtractCanonicalForSourceIDsFromQuerier(ctx context.Context, querier CanonicalQuerier, sourceIDs []string) ([]Artifact, error) {
	if querier == nil {
		return nil, fmt.Errorf("extract canonical manifest: nil querier")
	}
	scope, err := canonicalScopeForSourceIDs(sourceIDs)
	if err != nil {
		return nil, err
	}

	return extractCanonical(ctx, querier, scope)
}

// ExtractCanonicalForSourceIDsFromQuerierToWriter streams a scoped canonical
// parity manifest from a canonical query surface, usually a pinned read-only
// transaction.
func ExtractCanonicalForSourceIDsFromQuerierToWriter(ctx context.Context, querier CanonicalQuerier, sourceIDs []string, writer ArtifactWriter) error {
	if querier == nil {
		return fmt.Errorf("extract canonical manifest: nil querier")
	}
	if writer == nil {
		return fmt.Errorf("extract canonical manifest: nil artifact writer")
	}
	scope, err := canonicalScopeForSourceIDs(sourceIDs)
	if err != nil {
		return err
	}

	return extractCanonicalToWriter(ctx, querier, scope, nil, writer)
}

// ExtractCanonicalForSourceIDsFromQuerierToWriterFiltered streams a scoped
// canonical parity manifest through a sampled artifact-key filter.
func ExtractCanonicalForSourceIDsFromQuerierToWriterFiltered(ctx context.Context, querier CanonicalQuerier, sourceIDs []string, filter ArtifactKeyFilter, writer ArtifactWriter) error {
	if querier == nil {
		return fmt.Errorf("extract canonical manifest: nil querier")
	}
	if writer == nil {
		return fmt.Errorf("extract canonical manifest: nil artifact writer")
	}
	scope, err := canonicalScopeForSourceIDs(sourceIDs)
	if err != nil {
		return err
	}

	return extractCanonicalToWriter(ctx, querier, scope, filter, writer)
}

type canonicalSourceScope struct {
	where     string
	args      []any
	sourceIDs []string
}

func canonicalScopeForSourceIDs(sourceIDs []string) (canonicalSourceScope, error) {
	if len(sourceIDs) == 0 {
		return canonicalSourceScope{}, fmt.Errorf("extract canonical manifest: at least one source_id is required")
	}

	seen := make(map[string]struct{}, len(sourceIDs))
	args := make([]any, 0, len(sourceIDs))
	uniqueSourceIDs := make([]string, 0, len(sourceIDs))
	placeholders := make([]string, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		if sourceID == "" {
			return canonicalSourceScope{}, fmt.Errorf("extract canonical manifest: source_id must not be empty")
		}
		if _, ok := seen[sourceID]; ok {
			continue
		}
		seen[sourceID] = struct{}{}
		args = append(args, sourceID)
		uniqueSourceIDs = append(uniqueSourceIDs, sourceID)
		placeholders = append(placeholders, "?")
	}
	return canonicalSourceScope{
		where:     "WHERE s.id IN (" + strings.Join(placeholders, ", ") + ")",
		args:      args,
		sourceIDs: uniqueSourceIDs,
	}, nil
}

func extractCanonical(ctx context.Context, querier CanonicalQuerier, scope canonicalSourceScope) ([]Artifact, error) {
	var artifacts []Artifact
	err := extractCanonicalToWriter(ctx, querier, scope, nil, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	}))
	if err != nil {
		return nil, err
	}
	return artifacts, nil
}

func extractCanonicalToWriter(ctx context.Context, querier CanonicalQuerier, scope canonicalSourceScope, filter ArtifactKeyFilter, writer ArtifactWriter) error {
	if err := writeSessionArtifacts(ctx, querier, scope, filter, writer); err != nil {
		return err
	}
	if err := writeTurnArtifacts(ctx, querier, scope, filter, writer); err != nil {
		return err
	}
	if err := writeOpArtifacts(ctx, querier, scope, filter, writer); err != nil {
		return err
	}
	if err := writePayloadRefArtifacts(ctx, querier, scope, filter, writer); err != nil {
		return err
	}
	if err := writeLogEntryArtifacts(ctx, querier, scope, filter, writer); err != nil {
		return err
	}
	return nil
}

func canonicalScopedSQL(query string, scope canonicalSourceScope) string {
	if scope.where == "" {
		return query
	}
	orderBy := strings.LastIndex(query, "\nORDER BY ")
	if orderBy == -1 {
		return query + "\n" + scope.where
	}
	return query[:orderBy] + "\n" + scope.where + query[orderBy:]
}

func writePayloadRefArtifacts(ctx context.Context, querier CanonicalQuerier, scope canonicalSourceScope, filter ArtifactKeyFilter, writer ArtifactWriter) error {
	rows, err := querier.QueryContext(ctx, canonicalScopedSQL(canonicalPayloadRefsSQL, scope), scope.args...)
	if err != nil {
		return fmt.Errorf("query canonical payload refs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ordinals := map[string]int64{}
	resolver := newCanonicalPayloadResolver(canonicalPayloadArtifactMaxBytes)
	defer func() { _ = resolver.Close() }()
	for rows.Next() {
		row, scanErr := scanCanonicalPayloadRef(rows)
		if scanErr != nil {
			return scanErr
		}
		ordinalKey := payloadOrdinalKey(row)
		ordinals[ordinalKey]++
		identity, identityErr := payloadRefIdentity(row, ordinals[ordinalKey])
		if identityErr != nil {
			return identityErr
		}
		if filter != nil && !filter.IncludeClasslessKey(identity.classlessKey) {
			continue
		}
		class, classErr := payloadRefClass(row, identity.selector)
		if classErr != nil {
			return classErr
		}
		key := payloadRefMatchKey(identity.classlessKey, class)
		if filter != nil && !filter.IncludeArtifactKey(key, identity.classlessKey) {
			continue
		}
		artifact, buildErr := artifactFromPayloadRefResolved(row, identity, class, resolver)
		if buildErr != nil {
			return buildErr
		}
		if err := writeCanonicalArtifact(ctx, writer, artifact, "payload_ref"); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate canonical payload refs: %w", err)
	}
	return nil
}

func writeSessionArtifacts(ctx context.Context, querier CanonicalQuerier, scope canonicalSourceScope, filter ArtifactKeyFilter, writer ArtifactWriter) error {
	rows, err := querier.QueryContext(ctx, canonicalScopedSQL(canonicalSessionsSQL, scope), scope.args...)
	if err != nil {
		return fmt.Errorf("query canonical sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		row, scanErr := scanCanonicalSession(rows)
		if scanErr != nil {
			return scanErr
		}
		artifact, buildErr := artifactFromSession(row)
		if buildErr != nil {
			return buildErr
		}
		if err := writeCanonicalArtifactFiltered(ctx, writer, filter, artifact, "session"); err != nil {
			return err
		}
		if err := writeAdapterSessionArtifacts(ctx, writer, filter, row); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate canonical sessions: %w", err)
	}
	return nil
}

func writeAdapterSessionArtifacts(ctx context.Context, writer ArtifactWriter, filter ArtifactKeyFilter, row canonicalSessionRow) error {
	switch row.adapter {
	case aiAgentV2Format:
		return writeAIAgentV2SessionArtifacts(ctx, writer, filter, row)
	case aiAgentV3Format:
		artifact, ok, err := artifactFromAIAgentV3SessionMetadata(row)
		return writeOptionalCanonicalArtifact(ctx, writer, filter, artifact, ok, err, "aiagent_v3 session_metadata")
	case claudeCodeFormat:
		artifact, ok, err := artifactFromClaudeCodeSessionMetadata(row)
		return writeOptionalCanonicalArtifact(ctx, writer, filter, artifact, ok, err, "claude-code session_metadata")
	case codexFormat:
		artifact, ok, err := artifactFromCodexSessionMetadata(row)
		return writeOptionalCanonicalArtifact(ctx, writer, filter, artifact, ok, err, "codex session_metadata")
	case opencodeFormat:
		artifact, ok, err := artifactFromOpencodeSessionMetadata(row)
		return writeOptionalCanonicalArtifact(ctx, writer, filter, artifact, ok, err, "opencode session_metadata")
	default:
		return nil
	}
}

func writeAIAgentV2SessionArtifacts(ctx context.Context, writer ArtifactWriter, filter ArtifactKeyFilter, row canonicalSessionRow) error {
	report, ok, err := aiAgentV2FinalReportFromSessionExtras(row.extrasJSON)
	if err != nil {
		return err
	}
	if ok {
		artifact, buildErr := artifactFromAIAgentV2FinalReport(row, report)
		if buildErr != nil {
			return buildErr
		}
		if err := writeCanonicalArtifactFiltered(ctx, writer, filter, artifact, "aiagent_v2 final_report"); err != nil {
			return err
		}
	}

	artifact, ok, err := artifactFromAIAgentV2SessionMetadata(row)
	return writeOptionalCanonicalArtifact(ctx, writer, filter, artifact, ok, err, "aiagent_v2 session_metadata")
}

func writeOptionalCanonicalArtifact(ctx context.Context, writer ArtifactWriter, filter ArtifactKeyFilter, artifact Artifact, ok bool, buildErr error, label string) error {
	if buildErr != nil {
		return buildErr
	}
	if !ok {
		return nil
	}
	return writeCanonicalArtifactFiltered(ctx, writer, filter, artifact, label)
}

func writeCanonicalArtifactFiltered(ctx context.Context, writer ArtifactWriter, filter ArtifactKeyFilter, artifact Artifact, label string) error {
	if filter != nil && !filter.IncludeArtifactKey(artifact.Key(), artifact.ClasslessKey()) {
		return nil
	}
	return writeCanonicalArtifact(ctx, writer, artifact, label)
}

func writeCanonicalArtifact(ctx context.Context, writer ArtifactWriter, artifact Artifact, label string) error {
	if err := writer.WriteArtifact(ctx, artifact); err != nil {
		return fmt.Errorf("write canonical %s artifact: %w", label, err)
	}
	return nil
}

func writeTurnArtifacts(ctx context.Context, querier CanonicalQuerier, scope canonicalSourceScope, filter ArtifactKeyFilter, writer ArtifactWriter) error {
	rows, err := querier.QueryContext(ctx, canonicalScopedSQL(canonicalTurnsSQL, scope), scope.args...)
	if err != nil {
		return fmt.Errorf("query canonical turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		row, scanErr := scanCanonicalTurn(rows)
		if scanErr != nil {
			return scanErr
		}
		artifact, buildErr := artifactFromTurn(row)
		if buildErr != nil {
			return buildErr
		}
		if err := writeCanonicalArtifactFiltered(ctx, writer, filter, artifact, "turn"); err != nil {
			return err
		}
		if row.adapter == opencodeFormat {
			errorArtifact, ok, errorErr := artifactFromOpencodeAssistantError(row)
			if errorErr != nil {
				return errorErr
			}
			if ok {
				if err := writeCanonicalArtifactFiltered(ctx, writer, filter, errorArtifact, "opencode assistant_error"); err != nil {
					return err
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate canonical turns: %w", err)
	}
	return nil
}

func writeOpArtifacts(ctx context.Context, querier CanonicalQuerier, scope canonicalSourceScope, filter ArtifactKeyFilter, writer ArtifactWriter) error {
	rows, err := querier.QueryContext(ctx, canonicalScopedSQL(canonicalOpsSQL, scope), scope.args...)
	if err != nil {
		return fmt.Errorf("query canonical ops: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		row, scanErr := scanCanonicalOp(rows)
		if scanErr != nil {
			return scanErr
		}
		opArtifacts, buildErr := artifactsFromOp(row)
		if buildErr != nil {
			return buildErr
		}
		for _, artifact := range opArtifacts {
			if err := writeCanonicalArtifactFiltered(ctx, writer, filter, artifact, "op"); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate canonical ops: %w", err)
	}
	return nil
}

func writeLogEntryArtifacts(ctx context.Context, querier CanonicalQuerier, scope canonicalSourceScope, filter ArtifactKeyFilter, writer ArtifactWriter) error {
	for _, query := range canonicalLogEntryQueries(scope) {
		rows, err := querier.QueryContext(ctx, query.sql, query.args...)
		if err != nil {
			return fmt.Errorf("query canonical log entries: %w", err)
		}
		if err := writeLogEntryRows(ctx, rows, filter, writer); err != nil {
			return err
		}
	}
	return nil
}

func writeLogEntryRows(ctx context.Context, rows *sql.Rows, filter ArtifactKeyFilter, writer ArtifactWriter) error {
	for rows.Next() {
		row, scanErr := scanCanonicalLogEntry(rows)
		if scanErr != nil {
			_ = rows.Close()
			return scanErr
		}
		if err := writeLogEntryRowArtifacts(ctx, row, filter, writer); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate canonical log entries: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close canonical log entries: %w", err)
	}
	return nil
}

func writeLogEntryRowArtifacts(ctx context.Context, row canonicalLogEntryRow, filter ArtifactKeyFilter, writer ArtifactWriter) error {
	claudeSystemOp, err := isClaudeCodeSystemOpLogEntry(row)
	if err != nil {
		return err
	}
	if !claudeSystemOp {
		artifact, buildErr := artifactFromLogEntry(row)
		if buildErr != nil {
			return buildErr
		}
		if writeErr := writeCanonicalArtifactFiltered(ctx, writer, filter, artifact, "log_entry"); writeErr != nil {
			return writeErr
		}
	}
	return writeDerivedLogEntryArtifacts(ctx, row, filter, writer)
}

type canonicalDerivedLogArtifactFunc func(canonicalLogEntryRow) (Artifact, bool, error)

func writeDerivedLogEntryArtifacts(ctx context.Context, row canonicalLogEntryRow, filter ArtifactKeyFilter, writer ArtifactWriter) error {
	derived := []struct {
		label string
		build canonicalDerivedLogArtifactFunc
	}{
		{label: "claude-code system_op", build: artifactFromClaudeCodeSystemLogEntry},
		{label: "codex system_op", build: artifactFromCodexSystemLogEntry},
		{label: "opencode compaction_event", build: artifactFromOpencodeCompactionLogEntry},
		{label: "opencode attachment_metadata", build: artifactFromOpencodeAttachmentLogEntry},
		{label: "opencode session_message", build: artifactFromOpencodeSessionMessageLogEntry},
	}
	for _, item := range derived {
		artifact, ok, err := item.build(row)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := writeCanonicalArtifactFiltered(ctx, writer, filter, artifact, item.label); err != nil {
			return err
		}
	}
	return nil
}

type canonicalLogEntryQuery struct {
	sql  string
	args []any
}

func canonicalLogEntryQueries(scope canonicalSourceScope) []canonicalLogEntryQuery {
	if len(scope.sourceIDs) == 0 {
		return []canonicalLogEntryQuery{{
			sql:  canonicalScopedSQL(canonicalLogEntriesSQL, scope),
			args: scope.args,
		}}
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(scope.sourceIDs)), ",")
	args := make([]any, 0, len(scope.sourceIDs))
	for _, sourceID := range scope.sourceIDs {
		args = append(args, sourceID)
	}
	return []canonicalLogEntryQuery{
		{
			sql:  canonicalScopedLogEntriesDirectSQL(placeholders),
			args: append([]any(nil), args...),
		},
		{
			sql:  canonicalScopedLogEntriesSessionSQL(placeholders),
			args: append([]any(nil), args...),
		},
	}
}

const canonicalSessionsSQL = `
SELECT
	s.id,
	s.format,
	s.location,
	sess.id,
	sess.native_id,
		parent.native_id,
		root.native_id,
		sess.kind,
		sess.agent_name,
		sess.model,
		sess.provider_alias,
		sess.cwd,
		sess.call_path,
		sess.status,
		sess.start_ts,
		sess.end_ts,
	sess.extras_json
FROM sessions sess
JOIN sources s ON s.id = sess.source_id
JOIN sessions root ON root.id = sess.root_session_id
LEFT JOIN sessions parent ON parent.id = sess.parent_session_id
ORDER BY s.id, sess.native_id`

const canonicalTurnsSQL = `
SELECT
	s.id,
	s.format,
	s.location,
	sess.id,
	sess.native_id,
	t.id,
	t.seq,
	t.status,
	t.error_class,
	t.start_ts,
	t.end_ts,
	sess.status,
	sess.error_class,
	sess.error_message
FROM turns t
JOIN sessions sess ON sess.id = t.session_id
JOIN sources s ON s.id = sess.source_id
ORDER BY s.id, sess.native_id, t.seq`

const canonicalOpsSQL = `
SELECT
	s.id,
	s.format,
	s.location,
	sess.id,
	sess.native_id,
	t.id,
	t.seq,
	o.id,
	o.seq,
	o.kind,
	o.name,
	o.tool_namespace,
	o.status,
	o.error_class,
	o.error_message,
	o.start_ts,
	o.end_ts,
	o.bytes_in,
	o.bytes_out,
	child.native_id,
	o.extras_json
FROM ops o
JOIN turns t ON t.id = o.turn_id
JOIN sessions sess ON sess.id = o.session_id
JOIN sources s ON s.id = sess.source_id
LEFT JOIN sessions child ON child.id = o.child_session_id
ORDER BY s.id, sess.native_id, t.seq, o.seq`

const canonicalPayloadRefsSQL = `
SELECT
	s.id,
	s.format,
	s.location,
	sess.id,
	sess.native_id,
	t.id,
	t.seq,
	o.id,
	o.seq,
	o.kind,
	o.name,
	pr.id,
	pr.kind,
	pr.format,
	pr.compression,
	pr.location_uri,
	pr.original_bytes,
	pr.stored_bytes,
	pr.sha256
FROM payload_refs pr
JOIN ops o ON o.id = pr.op_id
JOIN turns t ON t.id = o.turn_id
JOIN sessions sess ON sess.id = o.session_id
JOIN sources s ON s.id = sess.source_id
ORDER BY s.id, sess.native_id, t.seq, o.seq, pr.id`

const canonicalLogEntriesSQL = `
SELECT
	l.id,
	l.ts,
	l.severity,
	l.source,
	l.message,
	l.extras_json,
	s.id,
	s.format,
	s.location,
	sess.id,
	sess.native_id,
	t.id,
	t.seq,
	o.id,
	o.seq
FROM log_entries l
LEFT JOIN sessions sess ON sess.id = l.session_id
LEFT JOIN turns t ON t.id = l.turn_id
LEFT JOIN ops o ON o.id = l.op_id
JOIN sources s ON s.id = COALESCE(l.source_id, sess.source_id)
ORDER BY s.id, sess.native_id, l.ts, l.id`

func canonicalScopedLogEntriesDirectSQL(placeholders string) string {
	return `
SELECT
	l.id,
	l.ts,
	l.severity,
	l.source,
	l.message,
	l.extras_json,
	s.id,
	s.format,
	s.location,
	sess.id,
	sess.native_id,
	t.id,
	t.seq,
	o.id,
	o.seq
FROM log_entries l
JOIN sources s ON s.id = l.source_id
LEFT JOIN sessions sess ON sess.id = l.session_id
LEFT JOIN turns t ON t.id = l.turn_id
LEFT JOIN ops o ON o.id = l.op_id
WHERE l.source_id IN (` + placeholders + `)
ORDER BY s.id, sess.native_id, l.ts, l.id`
}

func canonicalScopedLogEntriesSessionSQL(placeholders string) string {
	return `
SELECT
	l.id,
	l.ts,
	l.severity,
	l.source,
	l.message,
	l.extras_json,
	s.id,
	s.format,
	s.location,
	sess.id,
	sess.native_id,
	t.id,
	t.seq,
	o.id,
	o.seq
FROM sessions sess
JOIN sources s ON s.id = sess.source_id
JOIN log_entries l INDEXED BY idx_log_session_ts ON l.session_id = sess.id
LEFT JOIN turns t ON t.id = l.turn_id
LEFT JOIN ops o ON o.id = l.op_id
WHERE sess.source_id IN (` + placeholders + `)
  AND l.source_id IS NULL
ORDER BY s.id, sess.native_id, l.ts, l.id`
}

type canonicalPayloadRefRow struct {
	sourceID        string
	adapter         string
	sourceLocation  string
	sessionID       string
	nativeSessionID string
	turnID          string
	turnSeq         int64
	opID            string
	opSeq           int64
	opKind          string
	opName          string
	payloadRefID    int64
	kind            string
	format          string
	compression     sql.NullString
	locationURI     string
	originalBytes   sql.NullInt64
	storedBytes     sql.NullInt64
	sha256          sql.NullString
}

type canonicalSessionRow struct {
	sourceID              string
	adapter               string
	sourceLocation        string
	sessionID             string
	nativeSessionID       string
	parentNativeSessionID sql.NullString
	rootNativeSessionID   string
	kind                  string
	agentName             sql.NullString
	model                 sql.NullString
	providerAlias         sql.NullString
	cwd                   sql.NullString
	callPath              sql.NullString
	status                string
	startedAt             int64
	endedAt               sql.NullInt64
	extrasJSON            sql.NullString
}

type canonicalTurnRow struct {
	sourceID            string
	adapter             string
	sourceLocation      string
	sessionID           string
	nativeSessionID     string
	turnID              string
	turnSeq             int64
	status              string
	errorClass          sql.NullString
	startedAt           int64
	endedAt             sql.NullInt64
	sessionStatus       string
	sessionErrorClass   sql.NullString
	sessionErrorMessage sql.NullString
}

type canonicalOpRow struct {
	sourceID        string
	adapter         string
	sourceLocation  string
	sessionID       string
	nativeSessionID string
	turnID          string
	turnSeq         int64
	opID            string
	opSeq           int64
	kind            string
	name            string
	toolNamespace   sql.NullString
	status          string
	errorClass      sql.NullString
	errorMessage    sql.NullString
	startedAt       int64
	endedAt         sql.NullInt64
	bytesIn         int64
	bytesOut        int64
	childNativeID   sql.NullString
	extrasJSON      sql.NullString
}

type canonicalLogEntryRow struct {
	logID           int64
	ts              int64
	severity        string
	logSource       string
	message         string
	extrasJSON      sql.NullString
	sourceID        string
	adapter         string
	sourceLocation  string
	sessionID       sql.NullString
	nativeSessionID sql.NullString
	turnID          sql.NullString
	turnSeq         sql.NullInt64
	opID            sql.NullString
	opSeq           sql.NullInt64
}

type resolvedPayload struct {
	bytes      []byte
	hashDomain HashDomain
}

type payloadContainmentError struct {
	path string
	root string
}

func (e payloadContainmentError) Error() string {
	return fmt.Sprintf("payload_ref file %q resolves outside the source root %q", e.path, e.root)
}

func isPayloadContainmentError(err error) bool {
	var target payloadContainmentError
	return errors.As(err, &target)
}

func scanCanonicalPayloadRef(rows *sql.Rows) (canonicalPayloadRefRow, error) {
	var row canonicalPayloadRefRow
	if err := rows.Scan(
		&row.sourceID,
		&row.adapter,
		&row.sourceLocation,
		&row.sessionID,
		&row.nativeSessionID,
		&row.turnID,
		&row.turnSeq,
		&row.opID,
		&row.opSeq,
		&row.opKind,
		&row.opName,
		&row.payloadRefID,
		&row.kind,
		&row.format,
		&row.compression,
		&row.locationURI,
		&row.originalBytes,
		&row.storedBytes,
		&row.sha256,
	); err != nil {
		return canonicalPayloadRefRow{}, fmt.Errorf("scan canonical payload ref: %w", err)
	}
	return row, nil
}

func scanCanonicalSession(rows *sql.Rows) (canonicalSessionRow, error) {
	var row canonicalSessionRow
	if err := rows.Scan(
		&row.sourceID,
		&row.adapter,
		&row.sourceLocation,
		&row.sessionID,
		&row.nativeSessionID,
		&row.parentNativeSessionID,
		&row.rootNativeSessionID,
		&row.kind,
		&row.agentName,
		&row.model,
		&row.providerAlias,
		&row.cwd,
		&row.callPath,
		&row.status,
		&row.startedAt,
		&row.endedAt,
		&row.extrasJSON,
	); err != nil {
		return canonicalSessionRow{}, fmt.Errorf("scan canonical session: %w", err)
	}
	return row, nil
}

func scanCanonicalTurn(rows *sql.Rows) (canonicalTurnRow, error) {
	var row canonicalTurnRow
	if err := rows.Scan(
		&row.sourceID,
		&row.adapter,
		&row.sourceLocation,
		&row.sessionID,
		&row.nativeSessionID,
		&row.turnID,
		&row.turnSeq,
		&row.status,
		&row.errorClass,
		&row.startedAt,
		&row.endedAt,
		&row.sessionStatus,
		&row.sessionErrorClass,
		&row.sessionErrorMessage,
	); err != nil {
		return canonicalTurnRow{}, fmt.Errorf("scan canonical turn: %w", err)
	}
	return row, nil
}

func scanCanonicalOp(rows *sql.Rows) (canonicalOpRow, error) {
	var row canonicalOpRow
	if err := rows.Scan(
		&row.sourceID,
		&row.adapter,
		&row.sourceLocation,
		&row.sessionID,
		&row.nativeSessionID,
		&row.turnID,
		&row.turnSeq,
		&row.opID,
		&row.opSeq,
		&row.kind,
		&row.name,
		&row.toolNamespace,
		&row.status,
		&row.errorClass,
		&row.errorMessage,
		&row.startedAt,
		&row.endedAt,
		&row.bytesIn,
		&row.bytesOut,
		&row.childNativeID,
		&row.extrasJSON,
	); err != nil {
		return canonicalOpRow{}, fmt.Errorf("scan canonical op: %w", err)
	}
	return row, nil
}

func scanCanonicalLogEntry(rows *sql.Rows) (canonicalLogEntryRow, error) {
	var row canonicalLogEntryRow
	if err := rows.Scan(
		&row.logID,
		&row.ts,
		&row.severity,
		&row.logSource,
		&row.message,
		&row.extrasJSON,
		&row.sourceID,
		&row.adapter,
		&row.sourceLocation,
		&row.sessionID,
		&row.nativeSessionID,
		&row.turnID,
		&row.turnSeq,
		&row.opID,
		&row.opSeq,
	); err != nil {
		return canonicalLogEntryRow{}, fmt.Errorf("scan canonical log entry: %w", err)
	}
	return row, nil
}

type sessionBoundaryIdentity struct {
	NativeSessionID       string `json:"native_session_id"`
	ParentNativeSessionID string `json:"parent_native_session_id,omitempty"`
	RootNativeSessionID   string `json:"root_native_session_id"`
	Kind                  string `json:"kind"`
	Status                string `json:"status"`
	StartedAt             int64  `json:"started_at"`
	EndedAt               *int64 `json:"ended_at,omitempty"`
}

type turnBoundaryIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	TurnSeq         int64  `json:"turn_seq"`
	Status          string `json:"status"`
	StartedAt       int64  `json:"started_at"`
	EndedAt         *int64 `json:"ended_at,omitempty"`
}

type opBoundaryIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	TurnSeq         int64  `json:"turn_seq"`
	OpSeq           int64  `json:"op_seq"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	ToolNamespace   string `json:"tool_namespace,omitempty"`
	Status          string `json:"status"`
	StartedAt       int64  `json:"started_at"`
	EndedAt         *int64 `json:"ended_at,omitempty"`
}

type systemOpIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	TurnSeq         int64  `json:"turn_seq"`
	OpSeq           int64  `json:"op_seq"`
	OpKind          string `json:"op_kind"`
	Name            string `json:"name,omitempty"`
	Status          string `json:"status"`
	StartedAt       int64  `json:"started_at"`
	EndedAt         *int64 `json:"ended_at,omitempty"`
	OriginalKind    string `json:"original_kind,omitempty"`
}

type claudeCodeSystemOpIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	TurnSeq         int64  `json:"turn_seq,omitempty"`
	Subtype         string `json:"subtype,omitempty"`
	Severity        string `json:"severity"`
	Message         string `json:"message"`
	Timestamp       int64  `json:"timestamp"`
	ContentSHA256   string `json:"content_sha256,omitempty"`
}

type aiAgentV3SessionMetadataIdentity struct {
	NativeSessionID       string         `json:"native_session_id"`
	OriginID              string         `json:"origin_id"`
	AgentID               string         `json:"agent_id,omitempty"`
	CallPath              string         `json:"call_path,omitempty"`
	ParentNativeSessionID string         `json:"parent_native_session_id,omitempty"`
	ParentOpID            string         `json:"parent_op_id,omitempty"`
	HeadendID             string         `json:"headend_id,omitempty"`
	CapturePayloads       bool           `json:"capture_payloads"`
	Attributes            map[string]any `json:"attributes,omitempty"`
}

type aiAgentV3CompactionEventIdentity struct {
	NativeSessionID      string `json:"native_session_id"`
	TurnSeq              int64  `json:"turn_seq"`
	OpSeq                int64  `json:"op_seq"`
	Trigger              string `json:"trigger"`
	Name                 string `json:"name,omitempty"`
	Provider             string `json:"provider,omitempty"`
	ChildNativeSessionID string `json:"child_native_session_id,omitempty"`
	ArchivedTurn         int64  `json:"archived_turn,omitempty"`
	CurrentTurn          int64  `json:"current_turn,omitempty"`
	Status               string `json:"status"`
	StartedAt            int64  `json:"started_at"`
	EndedAt              *int64 `json:"ended_at,omitempty"`
}

type opErrorIdentity struct {
	NativeSessionID    string `json:"native_session_id"`
	TurnSeq            int64  `json:"turn_seq"`
	OpSeq              int64  `json:"op_seq"`
	OpKind             string `json:"op_kind"`
	ErrorClass         string `json:"error_class"`
	ErrorMessageSHA256 string `json:"error_message_sha256"`
}

type subagentLinkIdentity struct {
	ParentNativeSessionID string `json:"parent_native_session_id"`
	ParentTurnSeq         int64  `json:"parent_turn_seq"`
	ParentOpSeq           int64  `json:"parent_op_seq"`
	ChildNativeSessionID  string `json:"child_native_session_id"`
	LinkKind              string `json:"link_kind"`
	Direction             string `json:"direction"`
}

func artifactFromSession(row canonicalSessionRow) (Artifact, error) {
	parentNativeID, rootNativeID, err := canonicalSessionNativeLineage(row)
	if err != nil {
		return Artifact{}, err
	}
	identity := sessionBoundaryIdentity{
		NativeSessionID:       row.nativeSessionID,
		ParentNativeSessionID: parentNativeID,
		RootNativeSessionID:   rootNativeID,
		Kind:                  row.kind,
		Status:                row.status,
		StartedAt:             row.startedAt,
		EndedAt:               nullableInt64Ptr(row.endedAt),
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		nativeSessionID:    row.nativeSessionID,
		nativeArtifactID:   "session:" + row.nativeSessionID,
		class:              ClassSessionBoundary,
		selectorURI:        "canonical://sessions/" + url.PathEscape(row.sessionID),
		identity:           identity,
	})
	if err != nil {
		return Artifact{}, err
	}
	partial, err := aiAgentV3SyntheticSessionBoundary(row)
	if err != nil {
		return Artifact{}, err
	}
	if partial {
		artifact.Availability = AvailabilityPartialSource
	}
	return artifact, nil
}

func canonicalSessionNativeLineage(row canonicalSessionRow) (string, string, error) {
	parentNativeID := nullString(row.parentNativeSessionID)
	rootNativeID := row.rootNativeSessionID
	if !canonicalSessionNeedsStashedLineage(row, parentNativeID, rootNativeID) {
		return parentNativeID, rootNativeID, nil
	}
	fields, err := jsonObjectFromNullString(row.extrasJSON, row.adapter+" session lineage extras")
	if err != nil {
		return "", "", err
	}
	if parentNativeID == "" {
		stashedParent, err := aiViewerStringJSONField(fields, "parentNativeId")
		if err != nil {
			return "", "", err
		}
		parentNativeID = stashedParent
	}
	if rootNativeID == "" || rootNativeID == row.nativeSessionID {
		stashedRoot, err := aiViewerStringJSONField(fields, "rootNativeId")
		if err != nil {
			return "", "", err
		}
		if stashedRoot != "" {
			rootNativeID = stashedRoot
		}
	}
	return parentNativeID, rootNativeID, nil
}

func canonicalSessionNeedsStashedLineage(row canonicalSessionRow, parentNativeID string, rootNativeID string) bool {
	if parentNativeID == "" && row.kind != "root" {
		return true
	}
	if rootNativeID == "" {
		return true
	}
	return rootNativeID == row.nativeSessionID && row.kind != "root"
}

func aiAgentV3SyntheticSessionBoundary(row canonicalSessionRow) (bool, error) {
	if row.adapter != aiAgentV3Format {
		return false, nil
	}
	fields, err := jsonObjectFromNullString(row.extrasJSON, "aiagent_v3 synthetic session extras")
	if err != nil {
		return false, err
	}
	synthesized, ok, err := boolJSONField(fields, "synthesizedFromParent")
	if err != nil || !ok || !synthesized {
		return false, err
	}
	_, hasCapturePayloads, err := boolJSONField(fields, "capturePayloads")
	if err != nil {
		return false, err
	}
	return !hasCapturePayloads, nil
}

func artifactFromAIAgentV3SessionMetadata(row canonicalSessionRow) (Artifact, bool, error) {
	identity, ok, err := aiAgentV3SessionMetadataFromCanonical(row)
	if err != nil || !ok {
		return Artifact{}, ok, err
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		nativeSessionID:    row.nativeSessionID,
		nativeArtifactID:   "session:" + row.nativeSessionID + ":metadata",
		class:              ClassSessionMetadata,
		selectorURI:        "canonical://sessions/" + url.PathEscape(row.sessionID) + "#metadata",
		identity:           identity,
	})
	return artifact, true, err
}

func aiAgentV3SessionMetadataFromCanonical(row canonicalSessionRow) (aiAgentV3SessionMetadataIdentity, bool, error) {
	fields, err := jsonObjectFromNullString(row.extrasJSON, "aiagent_v3 session extras")
	if err != nil {
		return aiAgentV3SessionMetadataIdentity{}, false, err
	}
	if len(fields) == 0 {
		return aiAgentV3SessionMetadataIdentity{}, false, nil
	}
	capturePayloads, ok, err := boolJSONField(fields, "capturePayloads")
	if err != nil || !ok {
		return aiAgentV3SessionMetadataIdentity{}, ok, err
	}
	originID, err := stringJSONField(fields, "originId")
	if err != nil {
		return aiAgentV3SessionMetadataIdentity{}, false, err
	}
	if originID == "" {
		originID = row.rootNativeSessionID
		if originID == "" || originID == row.nativeSessionID {
			stashedRoot, err := aiViewerStringJSONField(fields, "rootNativeId")
			if err != nil {
				return aiAgentV3SessionMetadataIdentity{}, false, err
			}
			if stashedRoot != "" {
				originID = stashedRoot
			}
		}
	}
	parentOpID, err := stringJSONField(fields, "parentOpId")
	if err != nil {
		return aiAgentV3SessionMetadataIdentity{}, false, err
	}
	headendID, err := stringJSONField(fields, "headendId")
	if err != nil {
		return aiAgentV3SessionMetadataIdentity{}, false, err
	}
	parentNativeSessionID, err := stringJSONField(fields, "parentSessionId")
	if err != nil {
		return aiAgentV3SessionMetadataIdentity{}, false, err
	}
	attributes, err := prefixedJSONFields(fields, "attr.")
	if err != nil {
		return aiAgentV3SessionMetadataIdentity{}, false, err
	}
	return aiAgentV3SessionMetadataIdentity{
		NativeSessionID:       row.nativeSessionID,
		OriginID:              originID,
		AgentID:               nullString(row.agentName),
		CallPath:              nullString(row.callPath),
		ParentNativeSessionID: parentNativeSessionID,
		ParentOpID:            parentOpID,
		HeadendID:             headendID,
		CapturePayloads:       capturePayloads,
		Attributes:            attributes,
	}, true, nil
}

func artifactFromClaudeCodeSessionMetadata(row canonicalSessionRow) (Artifact, bool, error) {
	identity, ok, err := claudeCodeSessionMetadataFromCanonical(row)
	if err != nil || !ok {
		return Artifact{}, ok, err
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		nativeSessionID:    row.nativeSessionID,
		nativeArtifactID:   "session:" + row.nativeSessionID + ":metadata",
		class:              ClassSessionMetadata,
		selectorURI:        "canonical://sessions/" + url.PathEscape(row.sessionID) + "#metadata",
		identity:           identity,
	})
	return artifact, true, err
}

func claudeCodeSessionMetadataFromCanonical(row canonicalSessionRow) (claudeCodeSessionMetadataIdentity, bool, error) {
	fields, err := jsonObjectFromNullString(row.extrasJSON, "claude-code session extras")
	if err != nil {
		return claudeCodeSessionMetadataIdentity{}, false, err
	}
	if len(fields) == 0 {
		return claudeCodeSessionMetadataIdentity{}, false, nil
	}
	meta := claudeCodeSessionMetadataState{}
	lastPrompt, err := stringJSONField(fields, "lastPrompt")
	if err != nil {
		return claudeCodeSessionMetadataIdentity{}, false, err
	}
	if lastPrompt != "" {
		meta.lastPromptSHA256 = stringSHA256(lastPrompt)
	}
	if meta.customTitle, err = stringJSONField(fields, "customTitle"); err != nil {
		return claudeCodeSessionMetadataIdentity{}, false, err
	}
	if meta.aiTitle, err = stringJSONField(fields, "aiTitle"); err != nil {
		return claudeCodeSessionMetadataIdentity{}, false, err
	}
	if meta.permissionMode, err = stringJSONField(fields, "permissionMode"); err != nil {
		return claudeCodeSessionMetadataIdentity{}, false, err
	}
	if meta.bridgeSessionID, err = stringJSONField(fields, "bridge.bridgeSessionId"); err != nil {
		return claudeCodeSessionMetadataIdentity{}, false, err
	}
	if meta.bridgeLastSequenceNum, err = int64JSONField(fields, "bridge.lastSequenceNum"); err != nil {
		return claudeCodeSessionMetadataIdentity{}, false, err
	}
	if raw, ok := fields["fileHistory"]; ok && !jsonRawEmptyObjectOrNull(raw) {
		meta.fileHistorySHA256, err = canonicalJSONHash(raw)
		if err != nil {
			return claudeCodeSessionMetadataIdentity{}, false, fmt.Errorf("hash fileHistory metadata: %w", err)
		}
	}
	meta.prLinks, err = claudeCodePRLinksFromCanonical(fields)
	if err != nil {
		return claudeCodeSessionMetadataIdentity{}, false, err
	}
	ok := meta.lastPromptSHA256 != "" ||
		meta.customTitle != "" ||
		meta.aiTitle != "" ||
		meta.permissionMode != "" ||
		meta.bridgeSessionID != "" ||
		meta.bridgeLastSequenceNum != 0 ||
		meta.fileHistorySHA256 != "" ||
		len(meta.prLinks) > 0
	if !ok {
		return claudeCodeSessionMetadataIdentity{}, false, nil
	}
	return claudeCodeSessionMetadataIdentity{
		NativeSessionID:       row.nativeSessionID,
		LastPromptSHA256:      meta.lastPromptSHA256,
		CustomTitle:           meta.customTitle,
		AITitle:               meta.aiTitle,
		PermissionMode:        meta.permissionMode,
		BridgeSessionID:       meta.bridgeSessionID,
		BridgeLastSequenceNum: meta.bridgeLastSequenceNum,
		FileHistorySHA256:     meta.fileHistorySHA256,
		PRLinks:               meta.prLinks,
	}, true, nil
}

func claudeCodePRLinksFromCanonical(fields map[string]json.RawMessage) ([]claudeCodePRLinkIdentity, error) {
	raw, ok := fields["prLinks"]
	if !ok || jsonRawEmptyObjectOrNull(raw) {
		return nil, nil
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode prLinks: %w", err)
	}
	links := make([]claudeCodePRLinkIdentity, 0, len(rows))
	for _, row := range rows {
		number, err := int64JSONField(row, "prNumber")
		if err != nil {
			return nil, err
		}
		urlValue, err := stringJSONField(row, "prUrl")
		if err != nil {
			return nil, err
		}
		repo, err := stringJSONField(row, "prRepository")
		if err != nil {
			return nil, err
		}
		if number == 0 && urlValue == "" && repo == "" {
			continue
		}
		links = append(links, claudeCodePRLinkIdentity{
			Number:     number,
			URL:        urlValue,
			Repository: repo,
		})
	}
	return links, nil
}

func jsonObjectFromNullString(raw sql.NullString, label string) (map[string]json.RawMessage, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw.String), &fields); err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
	return fields, nil
}

func jsonRawEmptyObjectOrNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 ||
		bytes.Equal(trimmed, []byte("null")) ||
		bytes.Equal(trimmed, []byte("{}")) ||
		bytes.Equal(trimmed, []byte("[]"))
}

func stringJSONField(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("decode string field %q: %w", key, err)
	}
	return value, nil
}

func aiViewerStringJSONField(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields["aiViewer"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var aiViewer map[string]json.RawMessage
	if err := json.Unmarshal(raw, &aiViewer); err != nil {
		return "", fmt.Errorf("decode aiViewer object: %w", err)
	}
	return stringJSONField(aiViewer, key)
}

func boolJSONField(fields map[string]json.RawMessage, key string) (bool, bool, error) {
	raw, ok := fields[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false, fmt.Errorf("decode bool field %q: %w", key, err)
	}
	return value, true, nil
}

func int64JSONField(fields map[string]json.RawMessage, key string) (int64, error) {
	raw, ok := fields[key]
	if !ok {
		return 0, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, nil
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return 0, fmt.Errorf("decode integer field %q: %w", key, err)
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("decode integer field %q: %w", key, err)
		}
		return parsed, nil
	}
	parsed, err := strconv.ParseInt(string(trimmed), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("decode integer field %q: %w", key, err)
	}
	return parsed, nil
}

func prefixedJSONFields(fields map[string]json.RawMessage, prefix string) (map[string]any, error) {
	values := map[string]any{}
	for key, raw := range fields {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("decode prefixed field %q: %w", key, err)
		}
		values[strings.TrimPrefix(key, prefix)] = value
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values, nil
}

func artifactFromAIAgentV2FinalReport(row canonicalSessionRow, report []byte) (Artifact, error) {
	nativeSessionArtifactID := "session:" + row.nativeSessionID
	return canonicalJSONArtifact(canonicalJSONArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		nativeSessionID:    row.nativeSessionID,
		nativeArtifactID:   nativeSessionArtifactID + ":final_report",
		class:              ClassAssistantMessage,
		selector: Selector{
			URI:       aiAgentV2SelectorURI("sessions", row.nativeSessionID, nativeSessionArtifactID),
			FieldPath: "finalReport",
		},
		raw:   report,
		label: "aiagent_v2 final_report extras",
	})
}

func aiAgentV2FinalReportFromSessionExtras(extras sql.NullString) ([]byte, bool, error) {
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return nil, false, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(extras.String), &body); err != nil {
		return nil, false, fmt.Errorf("decode aiagent_v2 session extras: %w", err)
	}
	raw, ok := body["final_report"]
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return nil, false, nil
	}
	return raw, true, nil
}

func artifactFromTurn(row canonicalTurnRow) (Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", row.turnSeq)
	identity := turnBoundaryIdentity{
		NativeSessionID: row.nativeSessionID,
		TurnSeq:         row.turnSeq,
		Status:          row.status,
		StartedAt:       row.startedAt,
		EndedAt:         nullableInt64Ptr(row.endedAt),
	}
	return identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeTurnID,
		class:              ClassTurnBoundary,
		selectorURI:        "canonical://turns/" + url.PathEscape(row.turnID),
		identity:           identity,
	})
}

func artifactFromOpencodeAssistantError(row canonicalTurnRow) (Artifact, bool, error) {
	if row.status != "failed" || !row.errorClass.Valid {
		return Artifact{}, false, nil
	}
	timestamp := int64(0)
	if row.endedAt.Valid {
		timestamp = row.endedAt.Int64
	}
	errorClass := row.errorClass.String
	errorMessage := ""
	if row.sessionStatus == "failed" && row.sessionErrorClass.Valid && row.sessionErrorClass.String == errorClass && row.sessionErrorMessage.Valid {
		errorMessage = row.sessionErrorMessage.String
	}
	identity := opencodeAssistantErrorIdentity{
		NativeSessionID:    row.nativeSessionID,
		TurnSeq:            row.turnSeq,
		ErrorClass:         errorClass,
		ErrorMessageSHA256: stringSHA256(errorMessage),
		Timestamp:          timestamp,
	}
	nativeTurnID := opencodeNativeTurnID(row.turnSeq)
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   opencodeAssistantErrorNativeID(row.turnSeq),
		class:              ClassLLMError,
		selectorURI:        "canonical://turns/" + url.PathEscape(row.turnID) + "#assistant_error",
		identity:           identity,
	})
	return artifact, true, err
}

func artifactsFromOp(row canonicalOpRow) ([]Artifact, error) {
	nativeTurnID := fmt.Sprintf("turn:%d", row.turnSeq)
	nativeOpID := fmt.Sprintf("op:%d:%d", row.turnSeq, row.opSeq)
	toolNamespace := nullString(row.toolNamespace)
	if row.adapter == "codex" {
		toolNamespace = parityMCPToolNamespace(toolNamespace)
	}
	identity := opBoundaryIdentity{
		NativeSessionID: row.nativeSessionID,
		TurnSeq:         row.turnSeq,
		OpSeq:           row.opSeq,
		Kind:            row.kind,
		Name:            row.name,
		ToolNamespace:   toolNamespace,
		Status:          row.status,
		StartedAt:       row.startedAt,
		EndedAt:         nullableInt64Ptr(row.endedAt),
	}
	opArtifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeOpID,
		class:              ClassOpBoundary,
		selectorURI:        "canonical://ops/" + url.PathEscape(row.opID),
		identity:           identity,
	})
	if err != nil {
		return nil, err
	}
	artifacts := []Artifact{opArtifact}

	if row.status == "failed" && canonicalOpErrorSourceVisible(row) && (row.errorClass.Valid || row.errorMessage.Valid) {
		errorArtifact, buildErr := artifactFromOpError(row, nativeTurnID, nativeOpID)
		if buildErr != nil {
			return nil, buildErr
		}
		artifacts = append(artifacts, errorArtifact)
	}
	if row.childNativeID.Valid && row.childNativeID.String != "" {
		linkArtifact, buildErr := artifactFromSubagentLink(row, nativeTurnID, nativeOpID)
		if buildErr != nil {
			return nil, buildErr
		}
		artifacts = append(artifacts, linkArtifact)
	}
	adapterArtifacts, buildErr := adapterSpecificArtifactsFromOp(row, nativeTurnID, nativeOpID)
	if buildErr != nil {
		return nil, buildErr
	}
	artifacts = append(artifacts, adapterArtifacts...)

	return artifacts, nil
}

func canonicalOpErrorSourceVisible(row canonicalOpRow) bool {
	return row.adapter != codexFormat || row.kind != "llm"
}

func adapterSpecificArtifactsFromOp(row canonicalOpRow, nativeTurnID string, nativeOpID string) ([]Artifact, error) {
	switch row.adapter {
	case aiAgentV2Format:
		return aiAgentV2ArtifactsFromOp(row, nativeTurnID, nativeOpID)
	case aiAgentV3Format:
		return aiAgentV3ArtifactsFromOp(row, nativeTurnID, nativeOpID)
	case claudeCodeFormat:
		return claudeCodeArtifactsFromOp(row, nativeTurnID, nativeOpID)
	case codexFormat:
		return codexArtifactsFromOp(row, nativeTurnID, nativeOpID)
	case opencodeFormat:
		return opencodeArtifactsFromOp(row, nativeTurnID)
	default:
		return nil, nil
	}
}

func opencodeArtifactsFromOp(row canonicalOpRow, nativeTurnID string) ([]Artifact, error) {
	patches, err := opencodePatchMetadataFromExtras(row.extrasJSON)
	if err != nil {
		return nil, err
	}
	artifacts := make([]Artifact, 0, len(patches))
	for _, patch := range patches {
		patch.NativeSessionID = row.nativeSessionID
		patch.TurnSeq = row.turnSeq
		patch.OpSeq = row.opSeq
		artifact, buildErr := artifactFromOpencodePatchMetadata(row, nativeTurnID, patch)
		if buildErr != nil {
			return nil, buildErr
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func opencodePatchMetadataFromExtras(extras sql.NullString) ([]opencodePatchMetadataIdentity, error) {
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return nil, nil
	}
	var body struct {
		Patches []opencodePatchMetadataIdentity `json:"patches"`
	}
	if err := json.Unmarshal([]byte(extras.String), &body); err != nil {
		return nil, fmt.Errorf("decode opencode op extras: %w", err)
	}
	for i, patch := range body.Patches {
		if patch.PartID == "" {
			return nil, fmt.Errorf("decode opencode op extras patches[%d]: missing part_id", i)
		}
		if patch.FilesSHA256 == "" {
			return nil, fmt.Errorf("decode opencode op extras patches[%d]: missing files_sha256", i)
		}
	}
	return body.Patches, nil
}

func artifactFromOpencodePatchMetadata(row canonicalOpRow, nativeTurnID string, patch opencodePatchMetadataIdentity) (Artifact, error) {
	return identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   opencodePartNativeID(patch.PartID, "patch"),
		class:              ClassPatchMetadata,
		selectorURI:        opencodeSourceSelector("part", patch.PartID),
		identity:           patch,
	})
}

func aiAgentV2ArtifactsFromOp(row canonicalOpRow, nativeTurnID string, nativeOpID string) ([]Artifact, error) {
	var artifacts []Artifact
	if row.kind == "reasoning" {
		reasoningText, ok, parseErr := aiAgentV2ReasoningFinalFromExtras(row.extrasJSON)
		if parseErr != nil {
			return nil, parseErr
		}
		if ok {
			artifacts = append(artifacts, artifactFromAIAgentV2ReasoningFinal(row, nativeTurnID, nativeOpID, reasoningText))
		}
	}
	if row.kind == "system" {
		systemArtifact, buildErr := artifactFromAIAgentV2SystemOp(row, nativeTurnID, nativeOpID)
		if buildErr != nil {
			return nil, buildErr
		}
		artifacts = append(artifacts, systemArtifact)
	}
	if row.kind == "session" {
		compactionArtifact, ok, buildErr := artifactFromAIAgentV2CompactionEvent(row, nativeTurnID, nativeOpID)
		if buildErr != nil {
			return nil, buildErr
		}
		if ok {
			artifacts = append(artifacts, compactionArtifact)
		}
	}
	return artifacts, nil
}

func aiAgentV3ArtifactsFromOp(row canonicalOpRow, nativeTurnID string, nativeOpID string) ([]Artifact, error) {
	var artifacts []Artifact
	if row.kind == "system" {
		systemArtifact, buildErr := artifactFromAIAgentV3SystemOp(row, nativeTurnID, nativeOpID)
		if buildErr != nil {
			return nil, buildErr
		}
		artifacts = append(artifacts, systemArtifact)
	}
	if row.kind == "session" {
		compactionArtifact, ok, buildErr := artifactFromAIAgentV3CompactionEvent(row, nativeTurnID, nativeOpID)
		if buildErr != nil {
			return nil, buildErr
		}
		if ok {
			artifacts = append(artifacts, compactionArtifact)
		}
	}
	return artifacts, nil
}

func claudeCodeArtifactsFromOp(row canonicalOpRow, nativeTurnID string, nativeOpID string) ([]Artifact, error) {
	if row.kind != "compaction" || row.name != "compaction" {
		return nil, nil
	}
	artifact, err := artifactFromClaudeCodeCompactionEvent(row, nativeTurnID, nativeOpID)
	if err != nil {
		return nil, err
	}
	return []Artifact{artifact}, nil
}

func codexArtifactsFromOp(row canonicalOpRow, nativeTurnID string, nativeOpID string) ([]Artifact, error) {
	if row.kind != "compaction" || row.name != "compaction" {
		return nil, nil
	}
	artifact, err := artifactFromCodexCompactionEvent(row, nativeTurnID, nativeOpID)
	if err != nil {
		return nil, err
	}
	return []Artifact{artifact}, nil
}

func artifactFromAIAgentV3CompactionEvent(row canonicalOpRow, nativeTurnID string, nativeOpID string) (Artifact, bool, error) {
	fields, err := jsonObjectFromNullString(row.extrasJSON, "aiagent_v3 op extras")
	if err != nil {
		return Artifact{}, false, err
	}
	provider, err := stringJSONField(fields, "attr.provider")
	if err != nil {
		return Artifact{}, false, err
	}
	if provider != "history-compaction" {
		return Artifact{}, false, nil
	}
	archivedTurn, err := int64JSONField(fields, "attr.archivedTurn")
	if err != nil {
		return Artifact{}, false, err
	}
	currentTurn, err := int64JSONField(fields, "attr.currentTurn")
	if err != nil {
		return Artifact{}, false, err
	}
	name := row.name
	if name == "" {
		name, err = stringJSONField(fields, "attr.name")
		if err != nil {
			return Artifact{}, false, err
		}
	}
	identity := aiAgentV3CompactionEventIdentity{
		NativeSessionID:      row.nativeSessionID,
		TurnSeq:              row.turnSeq,
		OpSeq:                row.opSeq,
		Trigger:              "history_compaction",
		Name:                 name,
		Provider:             provider,
		ChildNativeSessionID: nullString(row.childNativeID),
		ArchivedTurn:         archivedTurn,
		CurrentTurn:          currentTurn,
		Status:               row.status,
		StartedAt:            row.startedAt,
		EndedAt:              nullableInt64Ptr(row.endedAt),
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeOpID + ":compaction",
		class:              ClassCompactionEvent,
		selectorURI:        "canonical://ops/" + url.PathEscape(row.opID) + "#compaction",
		identity:           identity,
	})
	return artifact, true, err
}

func artifactFromAIAgentV2CompactionEvent(row canonicalOpRow, nativeTurnID string, nativeOpID string) (Artifact, bool, error) {
	fields, err := jsonObjectFromNullString(row.extrasJSON, "aiagent_v2 op extras")
	if err != nil {
		return Artifact{}, false, err
	}
	stepKind, err := stringJSONField(fields, "step.kind")
	if err != nil {
		return Artifact{}, false, err
	}
	if stepKind != "internal" {
		return Artifact{}, false, nil
	}
	provider, err := stringJSONField(fields, "attr.provider")
	if err != nil {
		return Artifact{}, false, err
	}
	if provider != "history-compaction" {
		return Artifact{}, false, nil
	}
	archivedTurn, err := aiAgentV2JSONFieldWithFallback(fields, "attr.archivedTurn", "step.attr.archivedTurn")
	if err != nil {
		return Artifact{}, false, err
	}
	currentTurn, err := aiAgentV2JSONFieldWithFallback(fields, "attr.currentTurn", "step.attr.currentTurn")
	if err != nil {
		return Artifact{}, false, err
	}
	name := row.name
	if name == "" {
		name, err = stringJSONField(fields, "attr.name")
		if err != nil {
			return Artifact{}, false, err
		}
	}
	childNativeID := nullString(row.childNativeID)
	if childNativeID == "" {
		childNativeID, err = stringJSONField(fields, "childSessionRef")
		if err != nil {
			return Artifact{}, false, err
		}
	}
	identity := aiAgentV2CompactionEventIdentity{
		NativeSessionID:      row.nativeSessionID,
		TurnSeq:              row.turnSeq,
		OpSeq:                row.opSeq,
		Trigger:              "history_compaction",
		StepKind:             stepKind,
		Name:                 name,
		Provider:             provider,
		ChildNativeSessionID: childNativeID,
		ArchivedTurn:         archivedTurn,
		CurrentTurn:          currentTurn,
		Status:               row.status,
		StartedAt:            row.startedAt,
		EndedAt:              nullableInt64Ptr(row.endedAt),
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeOpID + ":compaction",
		class:              ClassCompactionEvent,
		selectorURI:        "canonical://ops/" + url.PathEscape(row.opID) + "#compaction",
		identity:           identity,
	})
	return artifact, true, err
}

func aiAgentV2JSONFieldWithFallback(fields map[string]json.RawMessage, primary string, fallback string) (int64, error) {
	if aiAgentV2AttrValuePresent(fields, primary) {
		return int64JSONField(fields, primary)
	}
	return int64JSONField(fields, fallback)
}

func artifactFromClaudeCodeCompactionEvent(row canonicalOpRow, nativeTurnID string, nativeOpID string) (Artifact, error) {
	meta, err := claudeCodeCompactionMetaFromExtras(row.extrasJSON)
	if err != nil {
		return Artifact{}, err
	}
	identity, err := claudeCodeCompactionEventIdentity(
		row.nativeSessionID,
		row.turnSeq,
		row.opSeq,
		meta,
		row.bytesIn,
		row.bytesOut,
		row.startedAt,
		nullableInt64Ptr(row.endedAt),
	)
	if err != nil {
		return Artifact{}, err
	}
	return identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeOpID + ":compaction",
		class:              ClassCompactionEvent,
		selectorURI:        "canonical://ops/" + url.PathEscape(row.opID) + "#compaction",
		identity:           identity,
	})
}

func claudeCodeCompactionMetaFromExtras(extras sql.NullString) (*claudeCodeCompactMeta, error) {
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return nil, nil
	}
	var meta claudeCodeCompactMeta
	if err := json.Unmarshal([]byte(extras.String), &meta); err != nil {
		return nil, fmt.Errorf("decode claude-code compaction metadata: %w", err)
	}
	return &meta, nil
}

func artifactFromCodexCompactionEvent(row canonicalOpRow, nativeTurnID string, nativeOpID string) (Artifact, error) {
	meta, err := codexCompactionMetaFromExtras(row.extrasJSON)
	if err != nil {
		return Artifact{}, err
	}
	identity := codexCompactionEventIdentityFor(
		row.nativeSessionID,
		row.turnSeq,
		row.opSeq,
		meta,
		row.startedAt,
		nullableInt64Ptr(row.endedAt),
	)
	return identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeOpID + ":compaction",
		class:              ClassCompactionEvent,
		selectorURI:        "canonical://ops/" + url.PathEscape(row.opID) + "#compaction",
		identity:           identity,
	})
}

func codexCompactionMetaFromExtras(extras sql.NullString) (codexCompactionEventMeta, error) {
	var meta codexCompactionEventMeta
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return meta, nil
	}
	var body struct {
		Trigger                string `json:"trigger"`
		ReplacementHistorySize int64  `json:"replacement_history_size"`
		MessagePreview         string `json:"message_preview"`
	}
	if err := json.Unmarshal([]byte(extras.String), &body); err != nil {
		return codexCompactionEventMeta{}, fmt.Errorf("decode codex compaction metadata: %w", err)
	}
	meta.Trigger = body.Trigger
	meta.ReplacementHistorySize = body.ReplacementHistorySize
	meta.MessagePreview = body.MessagePreview
	return meta, nil
}

func artifactFromAIAgentV2ReasoningFinal(row canonicalOpRow, nativeTurnID string, nativeOpID string, text string) Artifact {
	return semanticTextArtifact(semanticTextArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeOpID + ":reasoning.final",
		class:              ClassReasoningText,
		selector: Selector{
			URI:       aiAgentV2SelectorURI("ops", row.nativeSessionID, nativeOpID),
			FieldPath: "reasoning.final",
		},
		text: text,
	})
}

func aiAgentV2ReasoningFinalFromExtras(extras sql.NullString) (string, bool, error) {
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return "", false, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(extras.String), &body); err != nil {
		return "", false, fmt.Errorf("decode aiagent_v2 reasoning extras: %w", err)
	}
	raw, ok := body["reasoning.final"]
	if !ok {
		return "", false, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false, fmt.Errorf("decode aiagent_v2 reasoning.final: %w", err)
	}
	if text == "" {
		return "", false, nil
	}
	return text, true, nil
}

func artifactFromAIAgentV2SystemOp(row canonicalOpRow, nativeTurnID string, nativeOpID string) (Artifact, error) {
	originalKind, err := aiAgentV2OriginalKindFromOpExtras(row.extrasJSON)
	if err != nil {
		return Artifact{}, err
	}
	identity := systemOpIdentity{
		NativeSessionID: row.nativeSessionID,
		TurnSeq:         row.turnSeq,
		OpSeq:           row.opSeq,
		OpKind:          row.kind,
		Name:            row.name,
		Status:          row.status,
		StartedAt:       row.startedAt,
		EndedAt:         nullableInt64Ptr(row.endedAt),
		OriginalKind:    originalKind,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeOpID + ":system",
		class:              ClassSystemOp,
		selectorURI:        "canonical://ops/" + url.PathEscape(row.opID) + "#system",
		identity:           identity,
	})
}

func aiAgentV2OriginalKindFromOpExtras(extras sql.NullString) (string, error) {
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return "", nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(extras.String), &body); err != nil {
		return "", fmt.Errorf("decode aiagent_v2 op extras: %w", err)
	}
	raw, ok := body["original_kind"]
	if !ok {
		return "", nil
	}
	var kind string
	if err := json.Unmarshal(raw, &kind); err != nil {
		return "", fmt.Errorf("decode aiagent_v2 original_kind: %w", err)
	}
	return kind, nil
}

func artifactFromAIAgentV3SystemOp(row canonicalOpRow, nativeTurnID string, nativeOpID string) (Artifact, error) {
	identity := systemOpIdentity{
		NativeSessionID: row.nativeSessionID,
		TurnSeq:         row.turnSeq,
		OpSeq:           row.opSeq,
		OpKind:          row.kind,
		Name:            row.name,
		Status:          row.status,
		StartedAt:       row.startedAt,
		EndedAt:         nullableInt64Ptr(row.endedAt),
	}
	return identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeOpID + ":system",
		class:              ClassSystemOp,
		selectorURI:        "canonical://ops/" + url.PathEscape(row.opID) + "#system",
		identity:           identity,
	})
}

func artifactFromOpError(row canonicalOpRow, nativeTurnID string, nativeOpID string) (Artifact, error) {
	class := ClassToolError
	if row.kind == "llm" {
		class = ClassLLMError
	}
	identity := opErrorIdentity{
		NativeSessionID:    row.nativeSessionID,
		TurnSeq:            row.turnSeq,
		OpSeq:              row.opSeq,
		OpKind:             row.kind,
		ErrorClass:         nullString(row.errorClass),
		ErrorMessageSHA256: stringSHA256(nullString(row.errorMessage)),
	}
	return identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeOpID + ":error",
		class:              class,
		selectorURI:        "canonical://ops/" + url.PathEscape(row.opID) + "#error",
		identity:           identity,
	})
}

func artifactFromSubagentLink(row canonicalOpRow, nativeTurnID string, nativeOpID string) (Artifact, error) {
	identity := subagentLinkIdentity{
		ParentNativeSessionID: row.nativeSessionID,
		ParentTurnSeq:         row.turnSeq,
		ParentOpSeq:           row.opSeq,
		ChildNativeSessionID:  row.childNativeID.String,
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	}
	return identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		canonicalTurnID:    row.turnID,
		canonicalOpID:      row.opID,
		nativeSessionID:    row.nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeOpID + ":child_session:" + row.childNativeID.String,
		class:              ClassSubagentLink,
		selectorURI:        "canonical://ops/" + url.PathEscape(row.opID) + "#child_session",
		identity:           identity,
	})
}

func artifactFromLogEntry(row canonicalLogEntryRow) (Artifact, error) {
	nativeSessionID := "source:" + row.sourceID
	if row.nativeSessionID.Valid && row.nativeSessionID.String != "" {
		nativeSessionID = row.nativeSessionID.String
	}
	nativeTurnID := ""
	if row.turnSeq.Valid && row.turnSeq.Int64 > 0 {
		nativeTurnID = fmt.Sprintf("turn:%d", row.turnSeq.Int64)
	}
	scope := logEntryScope(row)
	nativeArtifactID := logNativeArtifactID(scope, row.ts, row.severity, row.logSource, row.message)
	selector := Selector{URI: logSelectorURI(row.sourceID, nativeArtifactID)}
	if parity := logEntryParitySelector(row.extrasJSON); parity.nativeArtifactID != "" && parity.selector.URI != "" {
		if parity.class == ClassAttachmentMetadata {
			return attachmentMetadataArtifactFromLogEntry(row, parity, nativeSessionID)
		}
		nativeArtifactID = parity.nativeArtifactID
		selector = parity.selector
	} else if row.adapter == "opencode" {
		meta, ok, err := opencodeLogPartMetadata(row.extrasJSON)
		if err != nil {
			return Artifact{}, err
		}
		if ok {
			selector = Selector{URI: opencodeSourceSelector("part", meta.partID)}
		}
	}
	messageBytes := []byte(row.message)
	return Artifact{
		SchemaVersion:      SchemaVersion,
		Adapter:            row.adapter,
		SourceID:           row.sourceID,
		SourceFile:         row.sourceLocation,
		CanonicalSessionID: nullString(row.sessionID),
		CanonicalTurnID:    nullString(row.turnID),
		CanonicalOpID:      nullString(row.opID),
		NativeSessionID:    nativeSessionID,
		NativeTurnID:       nativeTurnID,
		NativeArtifactID:   nativeArtifactID,
		Class:              ClassLogEntry,
		Availability:       logAvailability(messageBytes),
		HashDomain:         HashSemanticText,
		Selector:           selector,
		Bytes:              int64(len(messageBytes)),
		Chars:              int64(utf8.RuneCount(messageBytes)),
		ComputedSHA256:     stringSHA256(row.message),
		Synthetic:          false,
		SyntheticReason:    "",
	}, nil
}

func artifactFromClaudeCodeSystemLogEntry(row canonicalLogEntryRow) (Artifact, bool, error) {
	if row.adapter != "claude-code" {
		return Artifact{}, false, nil
	}
	meta, ok, err := claudeCodeSystemLogMetadata(row.extrasJSON)
	if err != nil || !ok {
		return Artifact{}, ok, err
	}
	if !claudeCodeLoggedSystemSubtype(meta.subtype) {
		return Artifact{}, false, nil
	}
	nativeSessionID := "source:" + row.sourceID
	if row.nativeSessionID.Valid && row.nativeSessionID.String != "" {
		nativeSessionID = row.nativeSessionID.String
	}
	turnSeq := nullInt64(row.turnSeq)
	scope := logEntryScope(row)
	nativeArtifactID := logNativeArtifactID(scope, row.ts, row.severity, row.logSource, row.message)
	selectorURI := logSelectorURI(row.sourceID, nativeArtifactID)
	if parity := logEntryParitySelector(row.extrasJSON); parity.class == ClassSystemOp && parity.nativeArtifactID != "" && parity.selector.URI != "" {
		nativeArtifactID = parity.nativeArtifactID
		selectorURI = parity.selector.URI
	}
	identity := claudeCodeSystemOpIdentity{
		NativeSessionID: nativeSessionID,
		TurnSeq:         turnSeq,
		Subtype:         meta.subtype,
		Severity:        row.severity,
		Message:         row.message,
		Timestamp:       row.ts,
		ContentSHA256:   optionalStringSHA256(meta.content),
	}
	nativeTurnID := ""
	if turnSeq > 0 {
		nativeTurnID = fmt.Sprintf("turn:%d", turnSeq)
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: nullString(row.sessionID),
		canonicalTurnID:    nullString(row.turnID),
		canonicalOpID:      nullString(row.opID),
		nativeSessionID:    nativeSessionID,
		nativeTurnID:       nativeTurnID,
		nativeArtifactID:   nativeArtifactID,
		class:              ClassSystemOp,
		selectorURI:        selectorURI,
		identity:           identity,
	})
	return artifact, true, err
}

func isClaudeCodeSystemOpLogEntry(row canonicalLogEntryRow) (bool, error) {
	if row.adapter != "claude-code" {
		return false, nil
	}
	meta, ok, err := claudeCodeSystemLogMetadata(row.extrasJSON)
	if err != nil || !ok {
		return ok, err
	}
	return claudeCodeLoggedSystemSubtype(meta.subtype), nil
}

type claudeCodeSystemLogMeta struct {
	subtype string
	content string
}

func claudeCodeSystemLogMetadata(extras sql.NullString) (claudeCodeSystemLogMeta, bool, error) {
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return claudeCodeSystemLogMeta{}, false, nil
	}
	var body struct {
		RecordType string `json:"recordType"`
		Subtype    string `json:"subtype"`
		Content    string `json:"content"`
	}
	if err := json.Unmarshal([]byte(extras.String), &body); err != nil {
		return claudeCodeSystemLogMeta{}, false, fmt.Errorf("decode claude-code system log extras: %w", err)
	}
	if body.RecordType != "system" {
		return claudeCodeSystemLogMeta{}, false, nil
	}
	return claudeCodeSystemLogMeta{
		subtype: body.Subtype,
		content: body.Content,
	}, true, nil
}

func claudeCodeLoggedSystemSubtype(subtype string) bool {
	switch subtype {
	case "compact_boundary", "api_error", "turn_duration":
		return false
	default:
		return true
	}
}

func optionalStringSHA256(value string) string {
	if value == "" {
		return ""
	}
	return stringSHA256(value)
}

type logEntryParityProof struct {
	class            ArtifactClass
	nativeArtifactID string
	selector         Selector
}

func logEntryParitySelector(extras sql.NullString) logEntryParityProof {
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return logEntryParityProof{}
	}
	var body struct {
		AIViewer struct {
			Parity struct {
				Class            string `json:"class"`
				NativeArtifactID string `json:"nativeArtifactId"`
				SelectorURI      string `json:"selectorURI"`
				JSONPointer      string `json:"jsonPointer"`
			} `json:"parity"`
		} `json:"aiViewer"`
	}
	if err := json.Unmarshal([]byte(extras.String), &body); err != nil {
		return logEntryParityProof{}
	}
	return logEntryParityProof{
		class:            ArtifactClass(body.AIViewer.Parity.Class),
		nativeArtifactID: body.AIViewer.Parity.NativeArtifactID,
		selector: Selector{
			URI:         body.AIViewer.Parity.SelectorURI,
			JSONPointer: body.AIViewer.Parity.JSONPointer,
		},
	}
}

func attachmentMetadataArtifactFromLogEntry(row canonicalLogEntryRow, parity logEntryParityProof, nativeSessionID string) (Artifact, error) {
	metadata := logEntryAttachmentMetadata(row.extrasJSON, nativeSessionID, nullInt64(row.turnSeq))
	return identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: nullString(row.sessionID),
		canonicalTurnID:    nullString(row.turnID),
		canonicalOpID:      nullString(row.opID),
		nativeSessionID:    nativeSessionID,
		nativeTurnID:       fmt.Sprintf("turn:%d", metadata.TurnSeq),
		nativeArtifactID:   parity.nativeArtifactID,
		class:              ClassAttachmentMetadata,
		selectorURI:        parity.selector.URI,
		identity:           metadata,
	})
}

func artifactFromOpencodeCompactionLogEntry(row canonicalLogEntryRow) (Artifact, bool, error) {
	if row.adapter != "opencode" || row.severity != "INF" {
		return Artifact{}, false, nil
	}
	meta, ok, err := opencodeLogPartMetadata(row.extrasJSON)
	if err != nil || !ok {
		return Artifact{}, ok, err
	}
	if meta.auto == nil {
		return Artifact{}, false, nil
	}
	message := opencodeCompactionLogMessage(*meta.auto)
	if row.message != message {
		return Artifact{}, false, nil
	}
	nativeSessionID := canonicalLogNativeSessionID(row)
	turnSeq := nullInt64(row.turnSeq)
	opSeq := nullInt64(row.opSeq)
	identity := opencodeCompactionEventIdentity{
		NativeSessionID: nativeSessionID,
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		Auto:            *meta.auto,
		Timestamp:       row.ts,
		Severity:        row.severity,
		Message:         row.message,
	}
	artifact, buildErr := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: nullString(row.sessionID),
		canonicalTurnID:    nullString(row.turnID),
		canonicalOpID:      nullString(row.opID),
		nativeSessionID:    nativeSessionID,
		nativeTurnID:       opencodeNativeTurnID(turnSeq),
		nativeArtifactID:   opencodePartNativeID(meta.partID, "compaction"),
		class:              ClassCompactionEvent,
		selectorURI:        opencodeSourceSelector("part", meta.partID),
		identity:           identity,
	})
	return artifact, true, buildErr
}

func artifactFromOpencodeAttachmentLogEntry(row canonicalLogEntryRow) (Artifact, bool, error) {
	if row.adapter != "opencode" || row.severity != "INF" || row.message != "file attachment" {
		return Artifact{}, false, nil
	}
	meta, ok, err := opencodeLogPartMetadata(row.extrasJSON)
	if err != nil || !ok {
		return Artifact{}, ok, err
	}
	nativeSessionID := canonicalLogNativeSessionID(row)
	turnSeq := nullInt64(row.turnSeq)
	opSeq := nullInt64(row.opSeq)
	identity := opencodeAttachmentMetadataIdentity{
		NativeSessionID: nativeSessionID,
		TurnSeq:         turnSeq,
		OpSeq:           opSeq,
		Filename:        meta.filename,
		URL:             meta.url,
		MIME:            meta.mime,
	}
	artifact, buildErr := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: nullString(row.sessionID),
		canonicalTurnID:    nullString(row.turnID),
		canonicalOpID:      nullString(row.opID),
		nativeSessionID:    nativeSessionID,
		nativeTurnID:       opencodeNativeTurnID(turnSeq),
		nativeArtifactID:   opencodePartNativeID(meta.partID, "file"),
		class:              ClassAttachmentMetadata,
		selectorURI:        opencodeSourceSelector("part", meta.partID),
		identity:           identity,
	})
	return artifact, true, buildErr
}

func artifactFromOpencodeSessionMessageLogEntry(row canonicalLogEntryRow) (Artifact, bool, error) {
	if row.adapter != "opencode" || row.severity != "INF" {
		return Artifact{}, false, nil
	}
	meta, ok, err := opencodeSessionMessageLogMetadata(row.extrasJSON)
	if err != nil || !ok {
		return Artifact{}, ok, err
	}
	expectedMessage, known := opencodeSessionMessageLogMessage(meta.eventType)
	if !known || row.message != expectedMessage {
		return Artifact{}, false, nil
	}
	nativeSessionID := canonicalLogNativeSessionID(row)
	identity := opencodeSessionMessageIdentity{
		NativeSessionID:  nativeSessionID,
		SessionMessageID: meta.sessionMessageID,
		EventType:        meta.eventType,
		Seq:              meta.seq,
		Timestamp:        row.ts,
		Severity:         row.severity,
		Message:          row.message,
		Agent:            meta.agent,
		ModelID:          meta.modelID,
		ProviderID:       meta.providerID,
		Variant:          meta.variant,
		DataSHA256:       meta.dataSHA256,
	}
	artifact, buildErr := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: nullString(row.sessionID),
		nativeSessionID:    nativeSessionID,
		nativeArtifactID:   "session_message:" + meta.sessionMessageID + ":system_op",
		class:              ClassSystemOp,
		selectorURI:        opencodeSourceSelector("session_message", meta.sessionMessageID),
		identity:           identity,
	})
	return artifact, true, buildErr
}

type opencodeLogPartMeta struct {
	partID   string
	auto     *bool
	filename string
	url      string
	mime     string
}

func opencodeLogPartMetadata(extras sql.NullString) (opencodeLogPartMeta, bool, error) {
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return opencodeLogPartMeta{}, false, nil
	}
	var body struct {
		PartID   string `json:"part_id"`
		Auto     *bool  `json:"auto"`
		Filename string `json:"filename"`
		URL      string `json:"url"`
		MIME     string `json:"mime"`
	}
	if err := json.Unmarshal([]byte(extras.String), &body); err != nil {
		return opencodeLogPartMeta{}, false, fmt.Errorf("decode opencode log extras: %w", err)
	}
	if body.PartID == "" {
		return opencodeLogPartMeta{}, false, nil
	}
	return opencodeLogPartMeta{
		partID:   body.PartID,
		auto:     body.Auto,
		filename: body.Filename,
		url:      body.URL,
		mime:     body.MIME,
	}, true, nil
}

type opencodeSessionMessageLogMeta struct {
	sessionMessageID string
	eventType        string
	seq              int64
	agent            string
	modelID          string
	providerID       string
	variant          string
	dataSHA256       string
}

func opencodeSessionMessageLogMetadata(extras sql.NullString) (opencodeSessionMessageLogMeta, bool, error) {
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return opencodeSessionMessageLogMeta{}, false, nil
	}
	var body struct {
		SessionMessageID   string `json:"session_message_id"`
		SessionMessageType string `json:"session_message_type"`
		Seq                int64  `json:"seq"`
		Agent              string `json:"agent"`
		ModelID            string `json:"model_id"`
		ProviderID         string `json:"provider_id"`
		Variant            string `json:"variant"`
		DataSHA256         string `json:"data_sha256"`
	}
	if err := json.Unmarshal([]byte(extras.String), &body); err != nil {
		return opencodeSessionMessageLogMeta{}, false, fmt.Errorf("decode opencode session_message log extras: %w", err)
	}
	if body.SessionMessageID == "" || body.SessionMessageType == "" {
		return opencodeSessionMessageLogMeta{}, false, nil
	}
	return opencodeSessionMessageLogMeta{
		sessionMessageID: body.SessionMessageID,
		eventType:        body.SessionMessageType,
		seq:              body.Seq,
		agent:            body.Agent,
		modelID:          body.ModelID,
		providerID:       body.ProviderID,
		variant:          body.Variant,
		dataSHA256:       body.DataSHA256,
	}, true, nil
}

func canonicalLogNativeSessionID(row canonicalLogEntryRow) string {
	if row.nativeSessionID.Valid && row.nativeSessionID.String != "" {
		return row.nativeSessionID.String
	}
	return "source:" + row.sourceID
}

func logEntryAttachmentMetadata(extras sql.NullString, nativeSessionID string, turnSeq int64) claudeCodeAttachmentMetadataIdentity {
	identity := claudeCodeAttachmentMetadataIdentity{
		NativeSessionID: nativeSessionID,
		TurnSeq:         turnSeq,
	}
	if !extras.Valid || strings.TrimSpace(extras.String) == "" {
		return identity
	}
	var body struct {
		AttachmentType string `json:"attachmentType"`
		Filename       string `json:"filename"`
		DisplayPath    string `json:"displayPath"`
	}
	if err := json.Unmarshal([]byte(extras.String), &body); err != nil {
		return identity
	}
	identity.AttachmentType = body.AttachmentType
	identity.Filename = body.Filename
	identity.DisplayPath = body.DisplayPath
	return identity
}

func logEntryScope(row canonicalLogEntryRow) string {
	if row.turnSeq.Valid && row.opSeq.Valid && row.turnSeq.Int64 > 0 && row.opSeq.Int64 > 0 {
		return fmt.Sprintf("op:%d:%d", row.turnSeq.Int64, row.opSeq.Int64)
	}
	if row.turnSeq.Valid && row.turnSeq.Int64 > 0 {
		return fmt.Sprintf("turn:%d", row.turnSeq.Int64)
	}
	if row.nativeSessionID.Valid && row.nativeSessionID.String != "" {
		return "session"
	}
	return "source"
}

func logNativeArtifactID(scope string, ts int64, severity string, source string, message string) string {
	sourceHash := sha256.Sum256([]byte(source))
	messageHash := sha256.Sum256([]byte(message))
	return fmt.Sprintf("log:%s:%d:%s:%x:%x", scope, ts, severity, sourceHash[:6], messageHash[:6])
}

func logSelectorURI(sourceID string, nativeArtifactID string) string {
	return "log://" + url.PathEscape(sourceID) + "/" + url.PathEscape(nativeArtifactID)
}

func logAvailability(message []byte) Availability {
	if len(message) == 0 {
		return AvailabilitySourceEmpty
	}
	return AvailabilityAvailable
}

type identityArtifactInput struct {
	sourceID           string
	adapter            string
	sourceFile         string
	canonicalSessionID string
	canonicalTurnID    string
	canonicalOpID      string
	nativeSessionID    string
	nativeTurnID       string
	nativeArtifactID   string
	class              ArtifactClass
	selectorURI        string
	identity           interface{}
}

func identityArtifact(in identityArtifactInput) (Artifact, error) {
	identityBytes, err := canonicalIdentityBytes(in.identity)
	if err != nil {
		return Artifact{}, err
	}
	sum := sha256.Sum256(identityBytes)
	return Artifact{
		SchemaVersion:      SchemaVersion,
		Adapter:            in.adapter,
		SourceID:           in.sourceID,
		SourceFile:         in.sourceFile,
		CanonicalSessionID: in.canonicalSessionID,
		CanonicalTurnID:    in.canonicalTurnID,
		CanonicalOpID:      in.canonicalOpID,
		NativeSessionID:    in.nativeSessionID,
		NativeTurnID:       in.nativeTurnID,
		NativeArtifactID:   in.nativeArtifactID,
		Class:              in.class,
		Availability:       AvailabilityAvailable,
		HashDomain:         HashIdentityJSON,
		Selector:           Selector{URI: in.selectorURI},
		Bytes:              int64(len(identityBytes)),
		Chars:              -1,
		ComputedSHA256:     fmt.Sprintf("%x", sum),
		Synthetic:          false,
		SyntheticReason:    "",
	}, nil
}

func canonicalIdentityBytes(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, fmt.Errorf("encode identity_json: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func artifactFromPayloadRefWithLimit(row canonicalPayloadRefRow, ordinal int64, maxBytes int64) (Artifact, error) {
	return artifactFromPayloadRefWithResolver(row, ordinal, newCanonicalPayloadResolver(maxBytes))
}

type canonicalPayloadRefIdentity struct {
	selector         Selector
	nativeArtifactID string
	classlessKey     ClasslessKey
}

func artifactFromPayloadRefWithResolver(row canonicalPayloadRefRow, ordinal int64, resolver *canonicalPayloadResolver) (Artifact, error) {
	identity, err := payloadRefIdentity(row, ordinal)
	if err != nil {
		return Artifact{}, err
	}
	class, err := payloadRefClass(row, identity.selector)
	if err != nil {
		return Artifact{}, err
	}
	return artifactFromPayloadRefResolved(row, identity, class, resolver)
}

func payloadRefIdentity(row canonicalPayloadRefRow, ordinal int64) (canonicalPayloadRefIdentity, error) {
	selector := Selector{}
	nativeArtifactID := opPayloadNativeID(row.turnSeq, row.opSeq, row.kind, ordinal)
	if strings.TrimSpace(row.locationURI) == "" {
		return payloadRefIdentityFromParts(row, selector, nativeArtifactID), nil
	}

	parsedSelector, parsedNativeID, err := selectorFromLocation(row.locationURI, row.payloadRefID)
	if err != nil {
		return canonicalPayloadRefIdentity{}, err
	}
	selector = parsedSelector
	nativeArtifactID = stablePayloadNativeArtifactID(row, selector, parsedNativeID)
	return payloadRefIdentityFromParts(row, selector, nativeArtifactID), nil
}

func payloadRefIdentityFromParts(row canonicalPayloadRefRow, selector Selector, nativeArtifactID string) canonicalPayloadRefIdentity {
	return canonicalPayloadRefIdentity{
		selector:         selector,
		nativeArtifactID: nativeArtifactID,
		classlessKey: ClasslessKey{
			SchemaVersion:    SchemaVersion,
			Adapter:          row.adapter,
			SourceID:         row.sourceID,
			NativeSessionID:  row.nativeSessionID,
			NativeArtifactID: nativeArtifactID,
		},
	}
}

func payloadRefMatchKey(classless ClasslessKey, class ArtifactClass) MatchKey {
	return MatchKey{
		SchemaVersion:    classless.SchemaVersion,
		Adapter:          classless.Adapter,
		SourceID:         classless.SourceID,
		NativeSessionID:  classless.NativeSessionID,
		Class:            class,
		NativeArtifactID: classless.NativeArtifactID,
	}
}

func artifactFromPayloadRefResolved(row canonicalPayloadRefRow, identity canonicalPayloadRefIdentity, class ArtifactClass, resolver *canonicalPayloadResolver) (Artifact, error) {
	if strings.TrimSpace(row.locationURI) == "" {
		return Artifact{
			SchemaVersion:      SchemaVersion,
			Adapter:            row.adapter,
			SourceID:           row.sourceID,
			SourceFile:         row.sourceLocation,
			CanonicalSessionID: row.sessionID,
			CanonicalTurnID:    row.turnID,
			CanonicalOpID:      row.opID,
			PayloadRefID:       row.payloadRefID,
			NativeSessionID:    row.nativeSessionID,
			NativeTurnID:       fmt.Sprintf("turn:%d", row.turnSeq),
			NativeArtifactID:   identity.nativeArtifactID,
			Class:              class,
			Availability:       AvailabilitySourceUnavailable,
			ProducerSHA256:     nullString(row.sha256),
			Synthetic:          false,
			SyntheticReason:    "",
		}, nil
	}

	if resolver == nil {
		resolver = newCanonicalPayloadResolver(canonicalPayloadArtifactMaxBytes)
	}
	resolved, resolveErr := resolver.resolve(row.sourceLocation, row.locationURI, row.compression.String)
	bytesLen := proofBytes(row.originalBytes, resolved.bytes, resolveErr)
	hash := proofHash(row.sha256, resolved.bytes, resolveErr)
	hashDomain := payloadHashDomain(row.kind, resolved.hashDomain)
	if isPayloadCapError(resolveErr) || isPayloadContainmentError(resolveErr) || (identity.selector.JSONPointer != "" && resolveErr != nil) {
		bytesLen = -1
		hash = ""
	}
	availability := payloadAvailability(bytesLen, hash)

	return Artifact{
		SchemaVersion:      SchemaVersion,
		Adapter:            row.adapter,
		SourceID:           row.sourceID,
		SourceFile:         sourceFileEvidence(row.sourceLocation, row.locationURI),
		CanonicalSessionID: row.sessionID,
		CanonicalTurnID:    row.turnID,
		CanonicalOpID:      row.opID,
		PayloadRefID:       row.payloadRefID,
		NativeSessionID:    row.nativeSessionID,
		NativeTurnID:       fmt.Sprintf("turn:%d", row.turnSeq),
		NativeArtifactID:   identity.nativeArtifactID,
		Class:              class,
		Availability:       availability,
		HashDomain:         hashDomain,
		Selector:           identity.selector,
		Bytes:              bytesLen,
		Chars:              payloadChars(hashDomain, resolved.bytes, resolveErr),
		ComputedSHA256:     hash,
		ProducerSHA256:     nullString(row.sha256),
		Synthetic:          false,
		SyntheticReason:    "",
	}, nil
}

func payloadRefClass(row canonicalPayloadRefRow, selector Selector) (ArtifactClass, error) {
	switch row.kind {
	case "llm_request":
		return ClassLLMRequest, nil
	case "llm_response":
		return payloadRefLLMResponseClass(row, selector), nil
	case "llm_sdk_request":
		return ClassLLMSDKRequest, nil
	case "llm_sdk_response":
		return ClassLLMSDKResponse, nil
	case "sdk_request":
		return ClassLLMSDKRequest, nil
	case "sdk_response":
		return ClassLLMSDKResponse, nil
	case "llm_reasoning":
		return ClassReasoningText, nil
	case "reasoning_stream":
		return ClassReasoningText, nil
	case "tool_request":
		return payloadRefToolRequestClass(row, selector), nil
	case "tool_response":
		return ClassToolResponse, nil
	case "log":
		return ClassLogEntry, nil
	default:
		return "", fmt.Errorf("canonical payload_ref kind %q is not mapped to a parity class", row.kind)
	}
}

func payloadRefLLMResponseClass(row canonicalPayloadRefRow, selector Selector) ArtifactClass {
	if row.adapter == "opencode" && row.opKind == "llm" && selector.FieldPath == "text" {
		return ClassAssistantMessage
	}
	if row.opKind == "llm" && row.opName == "message" && codexAssistantTextPointer(row.adapter, selector.JSONPointer) {
		return ClassAssistantMessage
	}
	if row.adapter == "claude-code" && row.opKind == "llm" && claudeCodeAssistantTextPointer(selector.JSONPointer) {
		return ClassAssistantMessage
	}
	return ClassLLMResponse
}

func payloadRefToolRequestClass(row canonicalPayloadRefRow, selector Selector) ArtifactClass {
	if row.opKind != "internal" || row.opName != "user_input" {
		return ClassToolRequest
	}
	if row.adapter == "claude-code" && claudeCodeUserImagePointer(selector.JSONPointer) {
		return ClassUserImage
	}
	if row.adapter == "codex" && codexUserImagePointer(selector.JSONPointer) {
		return ClassUserImage
	}
	if row.adapter == "opencode" && opencodeUserImageField(selector.FieldPath) {
		return ClassUserImage
	}
	return ClassUserPrompt
}

func opencodeUserImageField(field string) bool {
	const prefix = "prompt.files."
	if !strings.HasPrefix(field, prefix) {
		return false
	}
	index := strings.TrimPrefix(field, prefix)
	if index == "" || strings.Contains(index, ".") {
		return false
	}
	for _, r := range index {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func codexAssistantTextPointer(adapter string, pointer string) bool {
	if adapter != "codex" {
		return strings.HasPrefix(pointer, "/payload/content/")
	}
	return strings.HasPrefix(pointer, "/payload/content/") ||
		strings.HasPrefix(pointer, "/content/") ||
		codexLegacyAssistantTextPointer(pointer)
}

func codexLegacyAssistantTextPointer(pointer string) bool {
	if !strings.HasPrefix(pointer, "/items/") {
		return false
	}
	parts := strings.Split(pointer, "/")
	return len(parts) >= 5 && parts[3] == "content"
}

func codexUserImagePointer(pointer string) bool {
	for _, prefix := range []string{
		"/payload/content/",
		"/content/",
		"/payload/images/",
		"/payload/local_images/",
		"/payload/image_details/",
	} {
		if jsonPointerHasSingleIndexAfterPrefix(pointer, prefix) {
			return true
		}
	}
	for _, exact := range []string{"/payload/images", "/payload/local_images", "/payload/image_details"} {
		if pointer == exact {
			return true
		}
	}
	return false
}

func jsonPointerHasSingleIndexAfterPrefix(pointer string, prefix string) bool {
	if !strings.HasPrefix(pointer, prefix) {
		return false
	}
	index := strings.TrimPrefix(pointer, prefix)
	if index == "" || strings.Contains(index, "/") {
		return false
	}
	for _, r := range index {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func claudeCodeAssistantTextPointer(pointer string) bool {
	const prefix = "/message/content/"
	const suffix = "/text"
	if !strings.HasPrefix(pointer, prefix) || !strings.HasSuffix(pointer, suffix) {
		return false
	}
	index := strings.TrimSuffix(strings.TrimPrefix(pointer, prefix), suffix)
	if index == "" {
		return false
	}
	for _, r := range index {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func claudeCodeUserImagePointer(pointer string) bool {
	const prefix = "/message/content/"
	if !strings.HasPrefix(pointer, prefix) {
		return false
	}
	index := strings.TrimPrefix(pointer, prefix)
	if index == "" || strings.Contains(index, "/") {
		return false
	}
	for _, r := range index {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func payloadHashDomain(kind string, resolved HashDomain) HashDomain {
	if resolved != "" {
		return resolved
	}
	switch kind {
	case "llm_reasoning", "reasoning_stream", "log":
		return HashSemanticText
	default:
		return HashRawBytes
	}
}

func payloadOrdinalKey(row canonicalPayloadRefRow) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s", row.sourceID, row.nativeSessionID, row.turnSeq, row.opSeq, row.kind)
}

func opPayloadNativeID(turnSeq int64, opSeq int64, kind string, ordinal int64) string {
	return fmt.Sprintf("op:%d:%d:payload:%s:%d", turnSeq, opSeq, kind, ordinal)
}

func stablePayloadNativeArtifactID(row canonicalPayloadRefRow, selector Selector, fallback string) string {
	parsed, err := url.Parse(selector.URI)
	if err != nil || parsed.Scheme != "file" || parsed.Path == "" {
		return fallback
	}
	if selector.JSONPointer != "" || parsed.Fragment != "" {
		return fallback
	}
	return payloadFileNativeID(row.sourceLocation, parsed.Path)
}

func payloadFileNativeID(sourceLocation string, payloadPath string) string {
	root := sourceLocationPath(sourceLocation)
	if root != "" {
		rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(payloadPath))
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return "file:" + filepath.ToSlash(rel)
		}
	}
	return "file:" + filepath.Base(payloadPath)
}

func sourceLocationPath(sourceLocation string) string {
	if sourceLocation == "" {
		return ""
	}
	parsed, err := url.Parse(sourceLocation)
	if err == nil && parsed.Scheme == "file" && parsed.Path != "" {
		return parsed.Path
	}
	if filepath.IsAbs(sourceLocation) {
		return sourceLocation
	}
	return ""
}

func selectorFromLocation(locationURI string, payloadRefID int64) (Selector, string, error) {
	parsed, err := url.Parse(locationURI)
	if err != nil {
		return Selector{}, "", fmt.Errorf("parse payload_ref location_uri %q: %w", locationURI, err)
	}

	selectorURI := locationURI
	if parsed.Scheme == "file" {
		if pointer := parsed.Query().Get("json_pointer"); pointer != "" {
			values := parsed.Query()
			values.Del("json_pointer")
			normalized := *parsed
			normalized.RawQuery = values.Encode()
			selectorURI = normalized.String()
		}
	}
	selector := Selector{URI: selectorURI}
	if parsed.Scheme == "file" {
		selector.JSONPointer = parsed.Query().Get("json_pointer")
	}
	if line, ok := lineAnchor(parsed.Fragment); ok {
		if selector.JSONPointer != "" {
			return selector, fmt.Sprintf("line:%d:%s", line, selector.JSONPointer), nil
		}
		return selector, fmt.Sprintf("line:%d", line), nil
	}
	switch parsed.Scheme {
	case "file":
		if selector.JSONPointer != "" {
			return selector, fmt.Sprintf("file:%s:%s", filepath.Base(parsed.Path), selector.JSONPointer), nil
		}
	case "opencode-sqlite":
		values := parsed.Query()
		partID := values.Get("part_id")
		inputID := values.Get("input_id")
		field := values.Get("field")
		if field != "" {
			selector.FieldPath = field
		}
		if partID != "" && field != "" {
			return selector, fmt.Sprintf("part:%s:%s", partID, field), nil
		}
		if inputID != "" && field != "" {
			return selector, fmt.Sprintf("input:%s:%s", inputID, field), nil
		}
	}

	return selector, fmt.Sprintf("payload_ref:%d", payloadRefID), nil
}

func lineAnchor(fragment string) (int64, bool) {
	if !strings.HasPrefix(fragment, "L") {
		return 0, false
	}
	line, err := strconv.ParseInt(strings.TrimPrefix(fragment, "L"), 10, 64)
	if err != nil || line <= 0 {
		return 0, false
	}
	return line, true
}

const canonicalPayloadArtifactMaxBytes int64 = 1 << 30

type canonicalPayloadResolver struct {
	maxBytes        int64
	fileLineCache   map[fileLineCacheKey][]byte
	fileLineIndexes map[fileLineIndexKey][]int64
	activeLinePath  string
	activeLineFile  *os.File
}

type fileLineCacheKey struct {
	path     string
	fragment string
	maxBytes int64
}

type fileLineIndexKey struct {
	path     string
	maxBytes int64
}

func newCanonicalPayloadResolver(maxBytes int64) *canonicalPayloadResolver {
	return &canonicalPayloadResolver{
		maxBytes:        maxBytes,
		fileLineCache:   map[fileLineCacheKey][]byte{},
		fileLineIndexes: map[fileLineIndexKey][]int64{},
	}
}

func (r *canonicalPayloadResolver) Close() error {
	if r.activeLineFile == nil {
		return nil
	}
	err := r.activeLineFile.Close()
	r.activeLineFile = nil
	r.activeLinePath = ""
	return err
}

func (r *canonicalPayloadResolver) resolve(sourceLocation string, locationURI string, compression string) (resolvedPayload, error) {
	parsed, err := url.Parse(locationURI)
	if err != nil {
		return resolvedPayload{}, fmt.Errorf("parse payload_ref location_uri %q: %w", locationURI, err)
	}
	if parsed.Scheme == opencodeSQLiteScheme {
		return resolveOpencodeSQLitePayload(sourceLocation, parsed)
	}
	if parsed.Scheme != "file" {
		return resolvedPayload{}, fmt.Errorf("unsupported payload_ref location scheme %q", parsed.Scheme)
	}
	if parsed.Path == "" || !filepath.IsAbs(parsed.Path) {
		return resolvedPayload{}, fmt.Errorf("payload_ref file URI must be absolute")
	}

	path, err := canonicalPayloadPathWithinSource(sourceLocation, parsed.Path)
	if err != nil {
		return resolvedPayload{}, err
	}
	payload, err := r.readFileSelector(path, parsed.Fragment)
	if err != nil {
		return resolvedPayload{}, err
	}
	payload, err = decompressPayloadWithLimit(payload, compression, r.maxBytes)
	if err != nil {
		return resolvedPayload{}, err
	}
	pointer := parsed.Query().Get("json_pointer")
	if pointer == "" {
		return resolvedPayload{bytes: payload}, nil
	}
	return resolveJSONPointerPayload(payload, pointer)
}

func canonicalPayloadPathWithinSource(sourceLocation string, payloadPath string) (string, error) {
	root := sourceLocationPath(sourceLocation)
	if root == "" || !filepath.IsAbs(root) {
		return "", payloadContainmentError{path: payloadPath, root: sourceLocation}
	}
	resolvedRoot, err := evalSymlinksAllowingTail(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve payload_ref source root %q: %w", root, err)
	}
	resolvedPath, err := evalSymlinksAllowingTail(filepath.Clean(payloadPath))
	if err != nil {
		return "", fmt.Errorf("resolve payload_ref path %q: %w", payloadPath, err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return "", fmt.Errorf("relative payload_ref path %q under %q: %w", resolvedPath, resolvedRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", payloadContainmentError{path: resolvedPath, root: resolvedRoot}
	}
	return resolvedPath, nil
}

func evalSymlinksAllowingTail(abs string) (string, error) {
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return abs, nil
	}
	resolvedParent, err := evalSymlinksAllowingTail(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(abs)), nil
}

func (r *canonicalPayloadResolver) readFileSelector(path string, fragment string) ([]byte, error) {
	line, ok := lineAnchor(fragment)
	if !ok {
		return readFileSelectorWithLimit(path, fragment, r.maxBytes)
	}
	key := fileLineCacheKey{
		path:     filepath.Clean(path),
		fragment: fragment,
		maxBytes: r.maxBytes,
	}
	if cached, ok := r.fileLineCache[key]; ok {
		return append([]byte(nil), cached...), nil
	}
	payload, err := r.readIndexedLine(path, line)
	if err != nil {
		return nil, err
	}
	r.fileLineCache[key] = append([]byte(nil), payload...)
	return payload, nil
}

func (r *canonicalPayloadResolver) readIndexedLine(path string, line int64) ([]byte, error) {
	offsets, err := r.lineOffsets(path)
	if err != nil {
		return nil, err
	}
	if line <= 0 || line > int64(len(offsets)) {
		return nil, fmt.Errorf("payload_ref line %d not found", line)
	}
	file, err := r.activeFile(path)
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(offsets[line-1], io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek payload_ref line %d: %w", line, err)
	}
	reader := bufio.NewReader(file)
	lineBytes, err := readCanonicalLineSelectorLine(reader)
	if err != nil && len(lineBytes) == 0 {
		return nil, fmt.Errorf("read payload_ref file line: %w", err)
	}
	selected := trimLineEnding(lineBytes)
	if int64(len(selected)) > r.maxBytes {
		return nil, payloadCapError{label: "payload_ref", maxBytes: r.maxBytes}
	}
	return selected, nil
}

func (r *canonicalPayloadResolver) lineOffsets(path string) ([]int64, error) {
	key := fileLineIndexKey{path: filepath.Clean(path), maxBytes: r.maxBytes}
	if offsets, ok := r.fileLineIndexes[key]; ok {
		return offsets, nil
	}

	file, err := os.Open(path) // #nosec G304 -- path is resolved under sources.location by canonicalPayloadPathWithinSource before indexing.
	if err != nil {
		return nil, fmt.Errorf("open payload_ref file read-only: %w", err)
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(file)
	offsets := []int64{}
	var offset int64
	for {
		lineOffset := offset
		lineBytes, err := readCanonicalLineSelectorLine(reader)
		if err != nil && len(lineBytes) == 0 {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read payload_ref file line: %w", err)
		}
		offsets = append(offsets, lineOffset)
		offset += int64(len(lineBytes))
		if errors.Is(err, io.EOF) {
			break
		}
	}
	r.fileLineIndexes[key] = offsets
	return offsets, nil
}

func (r *canonicalPayloadResolver) activeFile(path string) (*os.File, error) {
	clean := filepath.Clean(path)
	if r.activeLineFile != nil && r.activeLinePath == clean {
		return r.activeLineFile, nil
	}
	if err := r.Close(); err != nil {
		return nil, fmt.Errorf("close previous payload_ref file: %w", err)
	}
	file, err := os.Open(path) // #nosec G304 -- path is resolved under sources.location by canonicalPayloadPathWithinSource before caching.
	if err != nil {
		return nil, fmt.Errorf("open payload_ref file read-only: %w", err)
	}
	r.activeLinePath = clean
	r.activeLineFile = file
	return file, nil
}

func readFileSelectorWithLimit(path string, fragment string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- callers pass paths resolved under sources.location or adapter source roots.
	if err != nil {
		return nil, fmt.Errorf("open payload_ref file read-only: %w", err)
	}
	defer func() { _ = file.Close() }()

	line, ok := lineAnchor(fragment)
	if !ok {
		if info, err := file.Stat(); err == nil && info.Mode().IsRegular() && info.Size() > maxBytes {
			return nil, payloadCapError{label: "payload_ref", maxBytes: maxBytes}
		}
		return readAllWithLimit(file, maxBytes, "payload_ref")
	}
	reader := bufio.NewReader(file)
	var current int64
	for {
		lineBytes, err := readCanonicalLineSelectorLine(reader)
		if err != nil && len(lineBytes) == 0 {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read payload_ref file line: %w", err)
		}
		current++
		if current == line {
			selected := trimLineEnding(lineBytes)
			if int64(len(selected)) > maxBytes {
				return nil, payloadCapError{label: "payload_ref", maxBytes: maxBytes}
			}
			return selected, nil
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return nil, fmt.Errorf("payload_ref line %d not found", line)
}

func trimLineEnding(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return line
}

func decompressPayloadWithLimit(payload []byte, compression string, maxBytes int64) ([]byte, error) {
	switch compression {
	case "", "none":
		if int64(len(payload)) > maxBytes {
			return nil, payloadCapError{label: "payload_ref", maxBytes: maxBytes}
		}
		return payload, nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("open gzip payload_ref: %w", err)
		}
		defer func() { _ = reader.Close() }()
		out, err := readAllWithLimit(reader, maxBytes, "decompressed payload_ref")
		if err != nil {
			return nil, fmt.Errorf("read gzip payload_ref: %w", err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported payload_ref compression %q", compression)
	}
}

func readAllWithLimit(reader io.Reader, maxBytes int64, label string) ([]byte, error) {
	if maxBytes < 0 {
		return nil, fmt.Errorf("%s limit must be non-negative", label)
	}
	out, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > maxBytes {
		return nil, payloadCapError{label: label, maxBytes: maxBytes}
	}
	return out, nil
}

type payloadCapError struct {
	label    string
	maxBytes int64
}

func (err payloadCapError) Error() string {
	return fmt.Sprintf("%s exceeds %d bytes", err.label, err.maxBytes)
}

func isPayloadCapError(err error) bool {
	var capErr payloadCapError
	return errors.As(err, &capErr)
}

func resolveJSONPointerPayload(payload []byte, pointer string) (resolvedPayload, error) {
	var doc interface{}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return resolvedPayload{}, fmt.Errorf("decode payload_ref JSON for pointer %q: %w", pointer, err)
	}

	value, err := jsonPointerValue(doc, pointer)
	if err != nil {
		return resolvedPayload{}, err
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

func jsonPointerValue(doc interface{}, pointer string) (interface{}, error) {
	if pointer == "" {
		return doc, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("json_pointer %q must start with /", pointer)
	}
	current := doc
	for _, rawToken := range strings.Split(pointer[1:], "/") {
		token, err := unescapeJSONPointerToken(rawToken)
		if err != nil {
			return nil, err
		}
		next, err := jsonPointerStep(current, token)
		if err != nil {
			return nil, fmt.Errorf("resolve json_pointer %q token %q: %w", pointer, token, err)
		}
		current = next
	}
	return current, nil
}

func jsonPointerStep(current interface{}, token string) (interface{}, error) {
	switch typed := current.(type) {
	case map[string]interface{}:
		value, ok := typed[token]
		if !ok {
			return nil, fmt.Errorf("object key not found")
		}
		return value, nil
	case []interface{}:
		index, err := parseJSONPointerIndex(token)
		if err != nil {
			return nil, err
		}
		if index >= len(typed) {
			return nil, fmt.Errorf("array index out of range")
		}
		return typed[index], nil
	default:
		return nil, fmt.Errorf("cannot descend into %T", current)
	}
}

func parseJSONPointerIndex(token string) (int, error) {
	if token == "" {
		return 0, fmt.Errorf("array index is empty")
	}
	if len(token) > 1 && token[0] == '0' {
		return 0, fmt.Errorf("array index has leading zero")
	}
	for _, r := range token {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("array index is not decimal")
		}
	}
	index, err := strconv.Atoi(token)
	if err != nil {
		return 0, fmt.Errorf("parse array index: %w", err)
	}
	return index, nil
}

func unescapeJSONPointerToken(token string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			out.WriteByte(token[i])
			continue
		}
		if i+1 >= len(token) {
			return "", fmt.Errorf("json_pointer token %q has trailing escape", token)
		}
		switch token[i+1] {
		case '0':
			out.WriteByte('~')
		case '1':
			out.WriteByte('/')
		default:
			return "", fmt.Errorf("json_pointer token %q has invalid escape", token)
		}
		i++
	}
	return out.String(), nil
}

func proofBytes(original sql.NullInt64, payload []byte, resolveErr error) int64 {
	if resolveErr == nil {
		return int64(len(payload))
	}
	if original.Valid && original.Int64 >= 0 {
		return original.Int64
	}
	return -1
}

func proofHash(producer sql.NullString, payload []byte, resolveErr error) string {
	if resolveErr == nil {
		sum := sha256.Sum256(payload)
		return fmt.Sprintf("%x", sum)
	}
	if producer.Valid && producer.String != "" {
		return producer.String
	}
	return ""
}

func payloadAvailability(bytesLen int64, hash string) Availability {
	if bytesLen == 0 && hash == EmptySHA256 {
		return AvailabilitySourceEmpty
	}
	if bytesLen >= 0 && hash != "" {
		return AvailabilityAvailable
	}
	return AvailabilityUnverifiable
}

func payloadChars(hashDomain HashDomain, payload []byte, resolveErr error) int64 {
	if resolveErr != nil || !utf8.Valid(payload) {
		return -1
	}
	if hashDomain == HashSemanticText {
		return int64(utf8.RuneCount(payload))
	}
	return -1
}

func sourceFileEvidence(sourceLocation string, locationURI string) string {
	parsed, err := url.Parse(locationURI)
	if err == nil && parsed.Scheme == "file" && parsed.Path != "" {
		return parsed.Path
	}
	return sourceLocation
}

func nullString(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

func nullInt64(v sql.NullInt64) int64 {
	if !v.Valid {
		return 0
	}
	return v.Int64
}

func parityMCPToolNamespace(namespace string) string {
	if strings.HasPrefix(namespace, "mcp:") {
		return namespace
	}
	return ""
}

func nullableInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func stringSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum)
}
