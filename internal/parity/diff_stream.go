package parity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // SQLite driver for temporary parity diff indexes.
)

const (
	streamDiffDBName  = "manifest-diff.db"
	streamDiffPattern = "ai-viewer-parity-diff-*"
	sideSource        = "source"
	sideCanonical     = "canonical"
)

// ArtifactReader streams artifacts into the disk-backed diff engine.
type ArtifactReader interface {
	NextArtifact(ctx context.Context) (Artifact, bool, error)
}

// StreamDiffOptions configures disk-backed manifest diffing.
type StreamDiffOptions struct {
	WorkDir     string
	MaxFindings int
}

type artifactSliceReader struct {
	artifacts []Artifact
	next      int
}

// StreamDiff accumulates source and canonical artifacts in a temporary SQLite
// index and computes the parity diff from that index.
type StreamDiff struct {
	db             *sql.DB
	cleanup        func()
	findings       *findingAccumulator
	tx             *sql.Tx
	stmt           *sql.Stmt
	writesFinished bool
	source         streamDiffSide
	canonical      streamDiffSide
}

type streamDiffSide struct {
	name  string
	count int
}

// NewArtifactSliceReader adapts fixture-sized artifact slices to ArtifactReader.
func NewArtifactSliceReader(artifacts []Artifact) ArtifactReader {
	return &artifactSliceReader{artifacts: artifacts}
}

func (r *artifactSliceReader) NextArtifact(ctx context.Context) (Artifact, bool, error) {
	if err := ctx.Err(); err != nil {
		return Artifact{}, false, err
	}
	if r.next >= len(r.artifacts) {
		return Artifact{}, false, nil
	}
	artifact := r.artifacts[r.next]
	r.next++
	return artifact, true, nil
}

// NewStreamDiff creates a disk-backed diff sink.
func NewStreamDiff(ctx context.Context, opts StreamDiffOptions) (*StreamDiff, error) {
	db, cleanup, err := openStreamDiffDB(ctx, opts.WorkDir)
	if err != nil {
		return nil, err
	}
	return &StreamDiff{
		db:       db,
		cleanup:  cleanup,
		findings: newFindingAccumulator(opts.MaxFindings),
		source: streamDiffSide{
			name: sideSource,
		},
		canonical: streamDiffSide{
			name: sideCanonical,
		},
	}, nil
}

// SourceWriter returns a writer for source artifacts.
func (d *StreamDiff) SourceWriter() ArtifactWriter {
	return ArtifactWriterFunc(d.WriteSourceArtifact)
}

// CanonicalWriter returns a writer for canonical artifacts.
func (d *StreamDiff) CanonicalWriter() ArtifactWriter {
	return ArtifactWriterFunc(d.WriteCanonicalArtifact)
}

// WriteSourceArtifact writes one source artifact into the disk-backed index.
func (d *StreamDiff) WriteSourceArtifact(ctx context.Context, artifact Artifact) error {
	return d.writeArtifact(ctx, &d.source, artifact)
}

// WriteCanonicalArtifact writes one canonical artifact into the disk-backed index.
func (d *StreamDiff) WriteCanonicalArtifact(ctx context.Context, artifact Artifact) error {
	return d.writeArtifact(ctx, &d.canonical, artifact)
}

// IngestSourceArtifacts writes all artifacts from a source reader.
func (d *StreamDiff) IngestSourceArtifacts(ctx context.Context, reader ArtifactReader) error {
	return d.ingestArtifactReader(ctx, &d.source, reader)
}

// IngestCanonicalArtifacts writes all artifacts from a canonical reader.
func (d *StreamDiff) IngestCanonicalArtifacts(ctx context.Context, reader ArtifactReader) error {
	return d.ingestArtifactReader(ctx, &d.canonical, reader)
}

// SourceCount returns the number of source artifacts written so far.
func (d *StreamDiff) SourceCount() int {
	return d.source.count
}

// CanonicalCount returns the number of canonical artifacts written so far.
func (d *StreamDiff) CanonicalCount() int {
	return d.canonical.count
}

// Result finishes writes and computes the parity result.
func (d *StreamDiff) Result(ctx context.Context) (Result, error) {
	if err := d.FinishWrites(ctx); err != nil {
		return Result{State: StateIncomplete}, err
	}
	if err := recordDuplicateKeys(ctx, d.db, d.findings); err != nil {
		return Result{State: StateIncomplete}, err
	}
	if err := compareSourceArtifacts(ctx, d.db, d.findings); err != nil {
		return Result{State: StateIncomplete}, err
	}
	if err := compareCanonicalArtifacts(ctx, d.db, d.findings); err != nil {
		return Result{State: StateIncomplete}, err
	}
	return d.findings.result(), nil
}

// FinishWrites commits the artifact insert transaction.
func (d *StreamDiff) FinishWrites(ctx context.Context) error {
	if d.writesFinished {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.stmt != nil {
		if err := d.stmt.Close(); err != nil {
			_ = d.tx.Rollback()
			return fmt.Errorf("close artifact insert: %w", err)
		}
		d.stmt = nil
		if err := d.tx.Commit(); err != nil {
			d.tx = nil
			return fmt.Errorf("commit artifact ingest: %w", err)
		}
		d.tx = nil
	}
	if err := createStreamDiffLookupIndexes(ctx, d.db); err != nil {
		return err
	}
	d.writesFinished = true
	return nil
}

// Close releases the temporary diff index. It is safe to call after Result.
func (d *StreamDiff) Close() {
	if d.stmt != nil {
		_ = d.stmt.Close()
		d.stmt = nil
	}
	if d.tx != nil {
		_ = d.tx.Rollback()
		d.tx = nil
	}
	if d.cleanup != nil {
		d.cleanup()
	}
}

func (d *StreamDiff) ingestArtifactReader(ctx context.Context, side *streamDiffSide, reader ArtifactReader) error {
	for i := 0; ; i++ {
		if err := checkDiffContext(ctx, i); err != nil {
			return err
		}
		artifact, ok, err := reader.NextArtifact(ctx)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if err := d.writeArtifact(ctx, side, artifact); err != nil {
			return err
		}
	}
	return nil
}

func (d *StreamDiff) writeArtifact(ctx context.Context, side *streamDiffSide, artifact Artifact) error {
	if err := d.ensureWriter(ctx); err != nil {
		return err
	}
	recordArtifactValidation(side.name, artifact, d.findings)
	raw, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("marshal %s artifact: %w", side.name, err)
	}
	if _, err := d.stmt.ExecContext(ctx, side.name, matchKeyString(artifact.Key()), classlessKeyString(artifact.ClasslessKey()), string(raw)); err != nil {
		return fmt.Errorf("insert %s artifact: %w", side.name, err)
	}
	side.count++
	return nil
}

func (d *StreamDiff) ensureWriter(ctx context.Context) error {
	if d.writesFinished {
		return fmt.Errorf("write artifact after finish")
	}
	if d.stmt != nil {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin artifact ingest: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO artifacts(side, match_key, classless_key, artifact_json) VALUES (?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare artifact insert: %w", err)
	}
	d.tx = tx
	d.stmt = stmt
	return nil
}

// DiffArtifactStreamsContext compares artifact streams through a temporary
// SQLite index instead of building in-memory match maps proportional to
// artifact count.
func DiffArtifactStreamsContext(ctx context.Context, source ArtifactReader, canonical ArtifactReader, opts StreamDiffOptions) (Result, error) {
	diff, err := NewStreamDiff(ctx, opts)
	if err != nil {
		return Result{State: StateIncomplete}, err
	}
	defer diff.Close()

	if err := diff.IngestSourceArtifacts(ctx, source); err != nil {
		return Result{State: StateIncomplete}, err
	}
	if err := diff.IngestCanonicalArtifacts(ctx, canonical); err != nil {
		return Result{State: StateIncomplete}, err
	}
	return diff.Result(ctx)
}

func openStreamDiffDB(ctx context.Context, workDir string) (*sql.DB, func(), error) {
	if workDir != "" {
		if err := os.MkdirAll(workDir, 0o700); err != nil {
			return nil, func() {}, fmt.Errorf("create parity diff work dir: %w", err)
		}
	}
	dir, err := os.MkdirTemp(workDir, streamDiffPattern)
	if err != nil {
		return nil, func() {}, fmt.Errorf("create parity diff temp dir: %w", err)
	}

	dbPath := filepath.Join(dir, streamDiffDBName)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, func() {}, fmt.Errorf("open parity diff db: %w", err)
	}
	cleanup := func() {
		_ = db.Close()
		_ = os.RemoveAll(dir)
	}
	if err := initStreamDiffDB(ctx, db); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return db, cleanup, nil
}

func initStreamDiffDB(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE artifacts (
			id INTEGER PRIMARY KEY,
			side TEXT NOT NULL,
			match_key TEXT NOT NULL,
			classless_key TEXT NOT NULL,
			artifact_json TEXT NOT NULL
		)`,
		`CREATE TABLE duplicate_keys (
			side TEXT NOT NULL,
			match_key TEXT NOT NULL,
			PRIMARY KEY (side, match_key)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("init parity diff db: %w", err)
		}
	}
	return nil
}

func createStreamDiffLookupIndexes(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`CREATE INDEX idx_artifacts_side_match ON artifacts(side, match_key)`,
		`CREATE INDEX idx_artifacts_side_classless ON artifacts(side, classless_key)`,
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create parity diff lookup index: %w", err)
		}
	}
	return nil
}

func recordArtifactValidation(side string, artifact Artifact, findings *findingAccumulator) {
	if err := artifact.Validate(); err != nil {
		code := CodeInvalidSourceArtifact
		if side == sideCanonical {
			code = CodeInvalidCanonicalArtifact
		}
		findings.add(newFinding(artifact, SeverityP1, code, err.Error()))
	}
	if err := validateArtifactAgainstMatrix(artifact); err != nil {
		findings.add(newFinding(artifact, SeverityP2, CodeMatrixMismatch, err.Error()))
	}
}

func recordDuplicateKeys(ctx context.Context, db *sql.DB, findings *findingAccumulator) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO duplicate_keys(side, match_key)
		SELECT side, match_key
		FROM artifacts
		GROUP BY side, match_key
		HAVING COUNT(*) > 1`); err != nil {
		return fmt.Errorf("record duplicate keys: %w", err)
	}
	for _, side := range []string{sideSource, sideCanonical} {
		if err := addDuplicateFindings(ctx, db, side, findings); err != nil {
			return err
		}
	}
	return nil
}

func addDuplicateFindings(ctx context.Context, db *sql.DB, side string, findings *findingAccumulator) error {
	rows, err := db.QueryContext(ctx, `SELECT match_key FROM duplicate_keys WHERE side = ? ORDER BY match_key`, side)
	if err != nil {
		return fmt.Errorf("query duplicate %s keys: %w", side, err)
	}
	defer func() { _ = rows.Close() }()

	code := CodeDuplicateSource
	if side == sideCanonical {
		code = CodeDuplicateCanonical
	}
	for i := 0; rows.Next(); i++ {
		if err := checkDiffContext(ctx, i); err != nil {
			return err
		}
		var key string
		if err := rows.Scan(&key); err != nil {
			return fmt.Errorf("scan duplicate %s key: %w", side, err)
		}
		artifact, err := firstArtifactByMatchKey(ctx, db, side, key)
		if err != nil {
			return err
		}
		findings.add(newFinding(artifact, SeverityP1, code, fmt.Sprintf("duplicate %s artifact key", side)))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate duplicate %s keys: %w", side, err)
	}
	return nil
}

func compareSourceArtifacts(ctx context.Context, db *sql.DB, findings *findingAccumulator) error {
	rows, err := db.QueryContext(ctx, `SELECT artifact_json, match_key, classless_key FROM artifacts WHERE side = ? ORDER BY id`, sideSource)
	if err != nil {
		return fmt.Errorf("query source artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for i := 0; rows.Next(); i++ {
		if err := checkDiffContext(ctx, i); err != nil {
			return err
		}
		source, matchKey, classlessKey, err := scanArtifactRow(rows)
		if err != nil {
			return err
		}
		if source.Availability == AvailabilitySourceCorrupt {
			findings.add(newFinding(source, SeverityP1, CodeSourceCorrupt, "source artifact is corrupt"))
			continue
		}
		if duplicate, err := hasDuplicateKey(ctx, db, sideSource, matchKey); err != nil {
			return err
		} else if duplicate {
			continue
		}

		canonicalCount, err := countByMatchKey(ctx, db, sideCanonical, matchKey)
		if err != nil {
			return err
		}
		if canonicalCount == 1 {
			canonical, err := firstArtifactByMatchKey(ctx, db, sideCanonical, matchKey)
			if err != nil {
				return err
			}
			findings.addAll(compareMatchedArtifacts(source, canonical))
			continue
		}
		if canonicalCount > 1 {
			findings.add(newFinding(source, SeverityP1, CodeDuplicateCanonical, "more than one canonical artifact matches source artifact"))
			continue
		}

		classlessCount, err := countByClasslessKey(ctx, db, sideCanonical, classlessKey)
		if err != nil {
			return err
		}
		if classlessCount > 0 {
			classlessMatch, err := firstArtifactByClasslessKey(ctx, db, sideCanonical, classlessKey)
			if err != nil {
				return err
			}
			findings.add(newFinding(source, SeverityP1, CodeClassMismatch, fmt.Sprintf("canonical class=%s, source class=%s", classlessMatch.Class, source.Class)))
			continue
		}

		findings.add(newFinding(source, missingSeverity(source), CodeMissingCanonical, "source artifact has no canonical match"))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate source artifacts: %w", err)
	}
	return nil
}

func compareCanonicalArtifacts(ctx context.Context, db *sql.DB, findings *findingAccumulator) error {
	rows, err := db.QueryContext(ctx, `SELECT artifact_json, match_key, classless_key FROM artifacts WHERE side = ? ORDER BY id`, sideCanonical)
	if err != nil {
		return fmt.Errorf("query canonical artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for i := 0; rows.Next(); i++ {
		if err := checkDiffContext(ctx, i); err != nil {
			return err
		}
		canonical, matchKey, classlessKey, err := scanArtifactRow(rows)
		if err != nil {
			return err
		}
		if duplicate, err := hasDuplicateKey(ctx, db, sideCanonical, matchKey); err != nil {
			return err
		} else if duplicate {
			continue
		}
		if count, err := countByMatchKey(ctx, db, sideSource, matchKey); err != nil {
			return err
		} else if count > 0 {
			continue
		}
		if count, err := countByClasslessKey(ctx, db, sideSource, classlessKey); err != nil {
			return err
		} else if count > 0 {
			continue
		}
		if syntheticIsDocumented(canonical) {
			continue
		}
		if canonical.Synthetic {
			findings.add(newFinding(canonical, SeverityP1, CodeUndocumentedSynthetic, "synthetic canonical artifact lacks a documented reason or id prefix"))
			continue
		}
		findings.add(newFinding(canonical, SeverityP1, CodeExtraCanonical, "canonical artifact has no source match"))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate canonical artifacts: %w", err)
	}
	return nil
}

func scanArtifactRow(scanner interface{ Scan(dest ...any) error }) (Artifact, string, string, error) {
	var raw, matchKey, classlessKey string
	if err := scanner.Scan(&raw, &matchKey, &classlessKey); err != nil {
		return Artifact{}, "", "", fmt.Errorf("scan artifact row: %w", err)
	}
	artifact, err := decodeArtifact(raw)
	if err != nil {
		return Artifact{}, "", "", err
	}
	return artifact, matchKey, classlessKey, nil
}

func firstArtifactByMatchKey(ctx context.Context, db *sql.DB, side string, key string) (Artifact, error) {
	row := db.QueryRowContext(ctx, `SELECT artifact_json, match_key, classless_key FROM artifacts WHERE side = ? AND match_key = ? ORDER BY id LIMIT 1`, side, key)
	artifact, _, _, err := scanArtifactRow(row)
	return artifact, err
}

func firstArtifactByClasslessKey(ctx context.Context, db *sql.DB, side string, key string) (Artifact, error) {
	row := db.QueryRowContext(ctx, `SELECT artifact_json, match_key, classless_key FROM artifacts WHERE side = ? AND classless_key = ? ORDER BY id LIMIT 1`, side, key)
	artifact, _, _, err := scanArtifactRow(row)
	return artifact, err
}

func countByMatchKey(ctx context.Context, db *sql.DB, side string, key string) (int, error) {
	return countByKey(ctx, db, `SELECT COUNT(*) FROM artifacts WHERE side = ? AND match_key = ?`, side, key)
}

func countByClasslessKey(ctx context.Context, db *sql.DB, side string, key string) (int, error) {
	return countByKey(ctx, db, `SELECT COUNT(*) FROM artifacts WHERE side = ? AND classless_key = ?`, side, key)
}

func hasDuplicateKey(ctx context.Context, db *sql.DB, side string, key string) (bool, error) {
	count, err := countByKey(ctx, db, `SELECT COUNT(*) FROM duplicate_keys WHERE side = ? AND match_key = ?`, side, key)
	return count > 0, err
}

func countByKey(ctx context.Context, db *sql.DB, query string, side string, key string) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, query, side, key).Scan(&count); err != nil {
		return 0, fmt.Errorf("count parity diff key: %w", err)
	}
	return count, nil
}

func decodeArtifact(raw string) (Artifact, error) {
	var artifact Artifact
	if err := json.Unmarshal([]byte(raw), &artifact); err != nil {
		return Artifact{}, fmt.Errorf("decode artifact row: %w", err)
	}
	return artifact, nil
}

func matchKeyString(key MatchKey) string {
	return stableKeyString(
		strconv.Itoa(key.SchemaVersion),
		key.Adapter,
		key.SourceID,
		key.NativeSessionID,
		string(key.Class),
		key.NativeArtifactID,
	)
}

func classlessKeyString(key ClasslessKey) string {
	return stableKeyString(
		strconv.Itoa(key.SchemaVersion),
		key.Adapter,
		key.SourceID,
		key.NativeSessionID,
		key.NativeArtifactID,
	)
}

func stableKeyString(parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
		b.WriteByte('|')
	}
	return b.String()
}
