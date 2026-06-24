// Package paritycheck wires the parity primitives into executable checks.
package paritycheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/ingest"
	"github.com/netdata/ai-viewer/internal/parity"
	"github.com/netdata/ai-viewer/internal/store"
)

const (
	tempDBName              = "parity-canonical.db"
	parityEventBuffer       = 1024
	parityBatchSize         = 256
	parityBatchInterval     = 10 * time.Millisecond
	parityResolverInterval  = time.Hour
	parityWorkDirPattern    = "ai-viewer-parity-*"
	defaultCheckSourceState = parity.StateIncomplete
)

const (
	stageCaptureSourceSnapshot = "capture_source_snapshot"
	stageExtractSourceManifest = "extract_source_manifest"
	stageExtractCanonical      = "extract_canonical_manifest"
	stageScanTempCanonicalDB   = "scan_temp_canonical_db"
	stageExtractCanonicalRows  = "extract_canonical_artifacts"
	stageVerifySourceSnapshot  = "verify_source_snapshot"
	stageDiffManifests         = "diff_manifests"
)

// Source identifies one configured source to check.
type Source struct {
	Format   string `json:"adapter"`
	Location string `json:"location"`
	SourceID string `json:"source_id"`
}

// Options configures one parity check run.
type Options struct {
	DBPath                 string
	WorkDir                string
	Sources                []Source
	Logger                 *slog.Logger
	MaxFindings            int
	SampleSize             int
	Concurrency            int
	ChangedSinceCutoffUS   int64
	ChangedSinceCursorPath string
	ResumePath             string
	AllowRepoOutput        bool

	snapshotHooks          sourceSnapshotHooks
	canonicalSnapshotHooks canonicalSnapshotHooks
}

type sourceSnapshotHooks struct {
	afterCapture func(Source)
}

type canonicalSnapshotHooks struct {
	afterPin func(Source)
}

// CheckResult is the machine-readable result emitted by the CLI.
type CheckResult struct {
	State          parity.ResultState `json:"state"`
	Sources        []SourceResult     `json:"sources"`
	TotalFindings  int                `json:"total_findings"`
	FindingSummary []FindingSummary   `json:"finding_summary,omitempty"`
	Findings       []parity.Finding   `json:"findings"`
}

// SourceResult is the result for one configured source.
type SourceResult struct {
	Adapter            string             `json:"adapter"`
	SourceID           string             `json:"source_id"`
	Location           string             `json:"location"`
	State              parity.ResultState `json:"state"`
	Skipped            bool               `json:"skipped,omitempty"`
	SkipReason         string             `json:"skip_reason,omitempty"`
	SourceArtifacts    int                `json:"source_artifacts"`
	CanonicalArtifacts int                `json:"canonical_artifacts"`
	TotalFindings      int                `json:"total_findings"`
	FindingSummary     []FindingSummary   `json:"finding_summary,omitempty"`
	Findings           []parity.Finding   `json:"findings,omitempty"`
	Errors             []string           `json:"errors,omitempty"`
	StageTimingsMS     map[string]int64   `json:"stage_timings_ms,omitempty"`
}

// FindingSummary is a grouped finding count.
type FindingSummary = parity.FindingSummary

// CheckSources runs source-vs-canonical parity for every configured source.
func CheckSources(ctx context.Context, opts Options) (CheckResult, error) {
	if len(opts.Sources) == 0 {
		return CheckResult{}, fmt.Errorf("at least one source is required")
	}
	if err := validateWorkDir(opts.WorkDir, opts.AllowRepoOutput); err != nil {
		return CheckResult{}, err
	}
	logger := checkLogger(opts.Logger)
	sources, err := normalizeSources(opts.Sources)
	if err != nil {
		return CheckResult{}, err
	}

	var existing *store.Store
	var existingDB *sql.DB
	if opts.DBPath != "" {
		existing, err = store.OpenReader(ctx, opts.DBPath, logger)
		if err != nil {
			return CheckResult{}, fmt.Errorf("open canonical db read-only: %w", err)
		}
		defer func() { _ = existing.Close() }()
		existingDB = existing.DB()
	}
	if opts.ChangedSinceCutoffUS > 0 && existingDB == nil {
		return CheckResult{}, fmt.Errorf("changed-since requires existing canonical db")
	}
	if opts.ResumePath != "" && opts.SampleSize > 0 {
		return CheckResult{}, fmt.Errorf("resume cannot be combined with sample mode")
	}
	if opts.ResumePath != "" && opts.ChangedSinceCutoffUS > 0 {
		return CheckResult{}, fmt.Errorf("resume cannot be combined with changed-since mode")
	}
	if opts.ResumePath != "" && opts.ChangedSinceCursorPath != "" {
		return CheckResult{}, fmt.Errorf("resume cannot be combined with changed-since mode")
	}
	if opts.ChangedSinceCutoffUS > 0 && opts.ChangedSinceCursorPath != "" {
		return CheckResult{}, fmt.Errorf("changed-since duration cannot be combined with changed-since cursor")
	}
	resume, err := openResumeState(opts.ResumePath)
	if err != nil {
		result := CheckResult{State: parity.StateIncomplete}
		for _, source := range sources {
			result.Sources = append(result.Sources, incompleteSourceResult(source, err))
		}
		return result, nil
	}
	changedSinceCursor, err := openResumeState(opts.ChangedSinceCursorPath)
	if err != nil {
		result := CheckResult{State: parity.StateIncomplete}
		for _, source := range sources {
			result.Sources = append(result.Sources, incompleteSourceResult(source, err))
		}
		return result, nil
	}

	result := CheckResult{State: parity.StatePass}
	for _, sourceResult := range checkSources(ctx, opts, logger, sources, existingDB, resume, changedSinceCursor) {
		result.Sources = append(result.Sources, sourceResult)
		result.TotalFindings += sourceResult.TotalFindings
		result.FindingSummary = mergeFindingSummaries(result.FindingSummary, sourceResult.FindingSummary)
		result.Findings = capFindings(append(result.Findings, sourceResult.Findings...), opts.MaxFindings)
	}
	result.State = aggregateState(result.Sources)
	return result, nil
}

func checkSources(ctx context.Context, opts Options, logger *slog.Logger, sources []Source, existingDB *sql.DB, resume *resumeState, changedSinceCursor *resumeState) []SourceResult {
	concurrency := normalizedConcurrency(opts.Concurrency)
	if concurrency == 1 || len(sources) <= 1 {
		results := make([]SourceResult, 0, len(sources))
		for _, source := range sources {
			results = append(results, checkSourceForRun(ctx, opts, logger, source, existingDB, resume, changedSinceCursor))
		}
		return results
	}

	results := make([]SourceResult, len(sources))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range sources {
		i, source := i, sources[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
				results[i] = checkSourceForRun(ctx, opts, logger, source, existingDB, resume, changedSinceCursor)
			case <-ctx.Done():
				results[i] = incompleteSourceResult(source, ctx.Err())
			}
		}()
	}
	wg.Wait()
	return results
}

func normalizedConcurrency(concurrency int) int {
	if concurrency <= 0 {
		return 1
	}
	return concurrency
}

func normalizeSources(in []Source) ([]Source, error) {
	out := make([]Source, 0, len(in))
	for _, source := range in {
		if source.Format == "" {
			return nil, fmt.Errorf("source format is required")
		}
		if source.Location == "" {
			return nil, fmt.Errorf("source location is required for %s", source.Format)
		}
		if _, ok := adapters.Get(source.Format); !ok {
			return nil, fmt.Errorf("unknown adapter format %q (registered: %v)", source.Format, adapters.Formats())
		}
		if _, err := os.Stat(source.Location); err != nil {
			return nil, fmt.Errorf("source %s:%s is not accessible: %w", source.Format, source.Location, err)
		}
		if source.SourceID == "" {
			source.SourceID = source.Format + ":" + source.Location
		}
		out = append(out, source)
	}
	return out, nil
}

func checkSourceForRun(ctx context.Context, opts Options, logger *slog.Logger, source Source, existingDB *sql.DB, resume *resumeState, changedSinceCursor *resumeState) SourceResult {
	if opts.ChangedSinceCutoffUS > 0 {
		changed, updatedAtUS, err := sourceChangedSince(ctx, existingDB, source, opts.ChangedSinceCutoffUS)
		if err != nil {
			return incompleteSourceResult(source, err)
		}
		if !changed {
			return skippedChangedSinceSourceResult(source, updatedAtUS, opts.ChangedSinceCutoffUS)
		}
	}

	result := newSourceResult(source)
	captureStarted := time.Now()
	capture, snapshotErr := captureSourceForRun(ctx, opts, source, existingDB)
	result.recordStageTiming(stageCaptureSourceSnapshot, captureStarted)
	defer capture.cleanup()
	if snapshotErr != nil {
		result = result.withError(fmt.Errorf("capture source snapshot: %w", snapshotErr))
	}
	if snapshotErr == nil && opts.snapshotHooks.afterCapture != nil {
		opts.snapshotHooks.afterCapture(source)
	}
	if snapshotErr == nil {
		if resumed, ok := resume.lookup(source, capture.snapshot); ok {
			if err := capture.snapshot.Verify(ctx); err != nil {
				return resumed.withError(fmt.Errorf("verify source snapshot: %w", err))
			}
			return resumed
		}
	}
	if snapshotErr == nil && changedSinceCursor.hasMatchingSourceSnapshot(source, capture.snapshot) {
		skipped := skippedChangedSinceCursorSourceResult(source)
		if err := capture.snapshot.Verify(ctx); err != nil {
			return skipped.withError(fmt.Errorf("verify source snapshot: %w", err))
		}
		return skipped
	}

	result = checkSource(ctx, opts, logger, capture.readSource, existingDB, capture.snapshot, snapshotErr, result, capture.verifyAfterExtraction)
	if changedSinceMode(opts) && result.State == parity.StatePass {
		result.State = parity.StateSampleOnly
	}
	if snapshotErr == nil {
		if err := resume.record(ctx, source, capture.snapshot, result); err != nil {
			result = result.withError(fmt.Errorf("write resume cursor: %w", err))
		}
	}
	return result
}

func changedSinceMode(opts Options) bool {
	return opts.ChangedSinceCutoffUS > 0 || opts.ChangedSinceCursorPath != ""
}

func sourceChangedSince(ctx context.Context, db *sql.DB, source Source, cutoffUS int64) (bool, int64, error) {
	var updatedAtUS int64
	err := db.QueryRowContext(ctx, `SELECT updated_at FROM source_progress WHERE source_id = ?`, source.SourceID).Scan(&updatedAtUS)
	if errors.Is(err, sql.ErrNoRows) {
		return true, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("select source_progress.updated_at: %w", err)
	}
	return updatedAtUS >= cutoffUS, updatedAtUS, nil
}

func skippedChangedSinceSourceResult(source Source, updatedAtUS int64, cutoffUS int64) SourceResult {
	return SourceResult{
		Adapter:    source.Format,
		SourceID:   source.SourceID,
		Location:   source.Location,
		State:      parity.StateSampleOnly,
		Skipped:    true,
		SkipReason: fmt.Sprintf("source_progress.updated_at=%d before changed_since_cutoff=%d", updatedAtUS, cutoffUS),
	}
}

func skippedChangedSinceCursorSourceResult(source Source) SourceResult {
	return SourceResult{
		Adapter:    source.Format,
		SourceID:   source.SourceID,
		Location:   source.Location,
		State:      parity.StateSampleOnly,
		Skipped:    true,
		SkipReason: "changed-since cursor source snapshot matched",
	}
}

func newSourceResult(source Source) SourceResult {
	return SourceResult{
		Adapter:  source.Format,
		SourceID: source.SourceID,
		Location: source.Location,
		State:    defaultCheckSourceState,
	}
}

func checkSource(
	ctx context.Context,
	opts Options,
	logger *slog.Logger,
	source Source,
	existingDB *sql.DB,
	snapshot sourceSnapshot,
	snapshotErr error,
	result SourceResult,
	verifyAfterExtraction bool,
) SourceResult {
	if opts.SampleSize == 0 {
		if existingDB != nil {
			return checkSourceWithExistingDBStream(ctx, opts, source, existingDB, snapshot, snapshotErr, result, verifyAfterExtraction)
		}
		return checkSourceWithTempDBStream(ctx, opts, logger, source, snapshot, snapshotErr, result, verifyAfterExtraction)
	}

	return checkSourceWithSampleStream(ctx, opts, logger, source, existingDB, snapshot, snapshotErr, result, verifyAfterExtraction)
}

func checkSourceWithExistingDBStream(
	ctx context.Context,
	opts Options,
	source Source,
	existingDB *sql.DB,
	snapshot sourceSnapshot,
	snapshotErr error,
	result SourceResult,
	verifyAfterExtraction bool,
) SourceResult {
	diff, err := parity.NewStreamDiff(ctx, parity.StreamDiffOptions{
		WorkDir:     opts.WorkDir,
		MaxFindings: opts.MaxFindings,
	})
	if err != nil {
		return result.withError(fmt.Errorf("open disk-backed diff: %w", err))
	}
	defer diff.Close()

	sourceStarted := time.Now()
	sourceCount, sourceErr := writeSourceArtifacts(ctx, source, diff.SourceWriter())
	result.recordStageTiming(stageExtractSourceManifest, sourceStarted)
	if sourceErr != nil {
		result = result.withError(fmt.Errorf("extract source manifest: %w", sourceErr))
	}
	canonicalStarted := time.Now()
	canonicalCount, canonicalErr := writeExistingCanonicalArtifacts(ctx, existingDB, source, opts.canonicalSnapshotHooks, diff.CanonicalWriter())
	result.recordStageTiming(stageExtractCanonical, canonicalStarted)
	if canonicalErr != nil {
		result = result.withError(fmt.Errorf("extract canonical manifest: %w", canonicalErr))
	}
	result.CanonicalArtifacts = canonicalCount
	result.SourceArtifacts = sourceCount
	if snapshotErr == nil && verifyAfterExtraction {
		verifyStarted := time.Now()
		if err := snapshot.Verify(ctx); err != nil {
			result = result.withError(fmt.Errorf("verify source snapshot: %w", err))
		}
		result.recordStageTiming(stageVerifySourceSnapshot, verifyStarted)
	}

	diffStarted := time.Now()
	diffResult, err := diff.Result(ctx)
	result.recordStageTiming(stageDiffManifests, diffStarted)
	if err != nil {
		return result.withError(fmt.Errorf("diff manifests: %w", err))
	}
	switch {
	case len(result.Errors) > 0:
		result.State = parity.StateIncomplete
	default:
		result.State = diffResult.State
	}
	result.TotalFindings = diffResult.TotalFindings
	result.FindingSummary = diffResult.FindingSummary
	result.Findings = capFindings(diffResult.Findings, opts.MaxFindings)
	return result
}

func checkSourceWithTempDBStream(
	ctx context.Context,
	opts Options,
	logger *slog.Logger,
	source Source,
	snapshot sourceSnapshot,
	snapshotErr error,
	result SourceResult,
	verifyAfterExtraction bool,
) SourceResult {
	diff, err := parity.NewStreamDiff(ctx, parity.StreamDiffOptions{
		WorkDir:     opts.WorkDir,
		MaxFindings: opts.MaxFindings,
	})
	if err != nil {
		return result.withError(fmt.Errorf("open disk-backed diff: %w", err))
	}
	defer diff.Close()

	sourceStarted := time.Now()
	sourceCount, sourceErr := writeSourceArtifacts(ctx, source, diff.SourceWriter())
	result.recordStageTiming(stageExtractSourceManifest, sourceStarted)
	if sourceErr != nil {
		result = result.withError(fmt.Errorf("extract source manifest: %w", sourceErr))
	}
	canonicalStarted := time.Now()
	canonicalCount, canonicalErr := writeTempCanonicalArtifactsWithTimings(ctx, opts, logger, source, nil, nil, diff.CanonicalWriter(), result.recordStageTiming)
	result.recordStageTiming(stageExtractCanonical, canonicalStarted)
	if canonicalErr != nil {
		result = result.withError(fmt.Errorf("extract canonical manifest: %w", canonicalErr))
	}
	result.CanonicalArtifacts = canonicalCount
	result.SourceArtifacts = sourceCount
	if snapshotErr == nil && verifyAfterExtraction {
		verifyStarted := time.Now()
		if err := snapshot.Verify(ctx); err != nil {
			result = result.withError(fmt.Errorf("verify source snapshot: %w", err))
		}
		result.recordStageTiming(stageVerifySourceSnapshot, verifyStarted)
	}

	diffStarted := time.Now()
	diffResult, err := diff.Result(ctx)
	result.recordStageTiming(stageDiffManifests, diffStarted)
	if err != nil {
		return result.withError(fmt.Errorf("diff manifests: %w", err))
	}
	switch {
	case len(result.Errors) > 0:
		result.State = parity.StateIncomplete
	default:
		result.State = diffResult.State
	}
	result.TotalFindings = diffResult.TotalFindings
	result.FindingSummary = diffResult.FindingSummary
	result.Findings = capFindings(diffResult.Findings, opts.MaxFindings)
	return result
}

func checkSourceWithSampleStream(
	ctx context.Context,
	opts Options,
	logger *slog.Logger,
	source Source,
	existingDB *sql.DB,
	snapshot sourceSnapshot,
	snapshotErr error,
	result SourceResult,
	verifyAfterExtraction bool,
) SourceResult {
	sampler := newBoundedSourceSampleWriter(opts.SampleSize)
	sourceWriter := parity.ArtifactWriter(sampler)
	if sampleSourceExtractionCanStopEarly(source) {
		sourceWriter = newEarlyStopSourceSampleWriter(sampler)
	}
	sourceStarted := time.Now()
	_, sourceErr := writeSourceArtifacts(ctx, source, sourceWriter)
	result.recordStageTiming(stageExtractSourceManifest, sourceStarted)
	if errors.Is(sourceErr, errSourceSampleComplete) {
		sourceErr = nil
	}
	if sourceErr != nil {
		result = result.withError(fmt.Errorf("extract source manifest: %w", sourceErr))
	}
	sourceArtifacts := sampler.Sample()
	result.SourceArtifacts = len(sourceArtifacts)
	filter := newSampledArtifactSet(sourceArtifacts)

	diff, err := parity.NewStreamDiff(ctx, parity.StreamDiffOptions{
		WorkDir:     opts.WorkDir,
		MaxFindings: opts.MaxFindings,
	})
	if err != nil {
		return result.withError(fmt.Errorf("open disk-backed diff: %w", err))
	}
	defer diff.Close()

	for _, artifact := range sourceArtifacts {
		if err := diff.SourceWriter().WriteArtifact(ctx, artifact); err != nil {
			return result.withError(fmt.Errorf("write sampled source artifact: %w", err))
		}
	}

	var canonicalCount int
	var canonicalErr error
	canonicalStarted := time.Now()
	switch {
	case existingDB != nil:
		canonicalCount, canonicalErr = writeExistingCanonicalArtifactsFiltered(ctx, existingDB, source, opts.canonicalSnapshotHooks, filter, diff.CanonicalWriter())
	case source.Format == "opencode":
		canonicalCount, canonicalErr = writeSampledOpencodeTempCanonicalArtifactsWithTimings(ctx, opts, logger, source, sourceArtifacts, filter, diff.CanonicalWriter(), result.recordStageTiming)
	default:
		scanCursor, _, cursorErr := sampledTempCanonicalScanCursor(ctx, source, sourceArtifacts)
		if cursorErr != nil {
			result = result.withError(fmt.Errorf("prepare sampled temp canonical scan cursor: %w", cursorErr))
		}
		canonicalCount, canonicalErr = writeTempCanonicalArtifactsWithTimings(ctx, opts, logger, source, filter, scanCursor, diff.CanonicalWriter(), result.recordStageTiming)
	}
	result.recordStageTiming(stageExtractCanonical, canonicalStarted)
	if canonicalErr != nil {
		result = result.withError(fmt.Errorf("extract canonical manifest: %w", canonicalErr))
	}
	result.CanonicalArtifacts = canonicalCount
	if snapshotErr == nil && verifyAfterExtraction {
		verifyStarted := time.Now()
		if err := snapshot.Verify(ctx); err != nil {
			result = result.withError(fmt.Errorf("verify source snapshot: %w", err))
		}
		result.recordStageTiming(stageVerifySourceSnapshot, verifyStarted)
	}

	diffStarted := time.Now()
	diffResult, err := diff.Result(ctx)
	result.recordStageTiming(stageDiffManifests, diffStarted)
	if err != nil {
		return result.withError(fmt.Errorf("diff manifests: %w", err))
	}
	switch {
	case len(result.Errors) > 0:
		result.State = parity.StateIncomplete
	default:
		result.State = parity.StateSampleOnly
	}
	result.TotalFindings = diffResult.TotalFindings
	result.FindingSummary = diffResult.FindingSummary
	result.Findings = capFindings(diffResult.Findings, opts.MaxFindings)
	return result
}

func sampleSourceExtractionCanStopEarly(source Source) bool {
	return source.Format == "aiagent_v2"
}

func writeSourceArtifacts(ctx context.Context, source Source, writer parity.ArtifactWriter) (int, error) {
	if writer == nil {
		return 0, fmt.Errorf("nil artifact writer")
	}

	counter := &countingArtifactWriter{writer: writer}
	switch source.Format {
	case "aiagent_v2":
		err := parity.ExtractAIAgentV2SourceToWriter(ctx, parity.AIAgentV2SourceOptions{
			Root:     source.Location,
			SourceID: source.SourceID,
		}, counter)
		return counter.count, err
	case "aiagent_v3":
		err := parity.ExtractAIAgentV3SourceToWriter(ctx, parity.AIAgentV3SourceOptions{
			Root:     source.Location,
			SourceID: source.SourceID,
		}, counter)
		return counter.count, err
	case "claude-code":
		err := parity.ExtractClaudeCodeSourceToWriter(ctx, parity.ClaudeCodeSourceOptions{
			Root:     source.Location,
			SourceID: source.SourceID,
		}, counter)
		return counter.count, err
	case "codex":
		err := parity.ExtractCodexSourceToWriter(ctx, parity.CodexSourceOptions{
			Root:     source.Location,
			SourceID: source.SourceID,
		}, counter)
		return counter.count, err
	case "opencode":
		err := parity.ExtractOpencodeSourceToWriter(ctx, parity.OpencodeSourceOptions{
			DBPath:   source.Location,
			SourceID: source.SourceID,
		}, counter)
		return counter.count, err
	default:
		artifacts, err := extractSourceArtifacts(ctx, source)
		for _, artifact := range artifacts {
			if writeErr := counter.WriteArtifact(ctx, artifact); writeErr != nil {
				return counter.count, writeErr
			}
		}
		return counter.count, err
	}
}

func writeTempCanonicalArtifacts(ctx context.Context, opts Options, logger *slog.Logger, source Source, writer parity.ArtifactWriter) (int, error) {
	return writeTempCanonicalArtifactsFiltered(ctx, opts, logger, source, nil, writer)
}

func writeTempCanonicalArtifactsFiltered(ctx context.Context, opts Options, logger *slog.Logger, source Source, filter parity.ArtifactKeyFilter, writer parity.ArtifactWriter) (int, error) {
	return writeTempCanonicalArtifactsWithTimings(ctx, opts, logger, source, filter, nil, writer, nil)
}

type stageTimingRecorder func(stage string, started time.Time)

func writeTempCanonicalArtifactsWithTimings(
	ctx context.Context,
	opts Options,
	logger *slog.Logger,
	source Source,
	filter parity.ArtifactKeyFilter,
	scanCursor canonical.Cursor,
	writer parity.ArtifactWriter,
	record stageTimingRecorder,
) (int, error) {
	if writer == nil {
		return 0, fmt.Errorf("extract canonical manifest: nil artifact writer")
	}

	workDir, cleanup, err := prepareWorkDir(opts.WorkDir)
	if err != nil {
		return 0, err
	}
	defer cleanup()

	st, err := store.OpenWriter(ctx, filepath.Join(workDir, tempDBName), logger)
	if err != nil {
		return 0, fmt.Errorf("open temp canonical db: %w", err)
	}
	defer func() { _ = st.Close() }()

	scanStarted := time.Now()
	scanErr := scanSourceIntoDBWithCursor(ctx, st.DB(), logger, source, scanCursor)
	recordStageTiming(record, stageScanTempCanonicalDB, scanStarted)
	extractStarted := time.Now()
	count, extractErr := writeExistingCanonicalArtifactsFiltered(ctx, st.DB(), source, canonicalSnapshotHooks{}, filter, writer)
	recordStageTiming(record, stageExtractCanonicalRows, extractStarted)
	if scanErr != nil || extractErr != nil {
		return count, errors.Join(scanErr, extractErr)
	}
	return count, nil
}

func writeExistingCanonicalArtifacts(ctx context.Context, db *sql.DB, source Source, hooks canonicalSnapshotHooks, writer parity.ArtifactWriter) (int, error) {
	return writeExistingCanonicalArtifactsFiltered(ctx, db, source, hooks, nil, writer)
}

func writeExistingCanonicalArtifactsFiltered(ctx context.Context, db *sql.DB, source Source, hooks canonicalSnapshotHooks, filter parity.ArtifactKeyFilter, writer parity.ArtifactWriter) (int, error) {
	if writer == nil {
		return 0, fmt.Errorf("extract canonical manifest: nil artifact writer")
	}

	tx, err := beginCanonicalReadSnapshot(ctx, db)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if hooks.afterPin != nil {
		hooks.afterPin(source)
	}

	counter := &countingArtifactWriter{writer: writer}
	extractErr := parity.ExtractCanonicalForSourceIDsFromQuerierToWriterFiltered(ctx, tx, []string{source.SourceID}, filter, counter)
	if extractErr != nil {
		return counter.count, extractErr
	}
	if err := tx.Commit(); err != nil {
		return counter.count, fmt.Errorf("close canonical read snapshot: %w", err)
	}
	committed = true
	return counter.count, nil
}

type countingArtifactWriter struct {
	writer parity.ArtifactWriter
	count  int
}

func (w *countingArtifactWriter) WriteArtifact(ctx context.Context, artifact parity.Artifact) error {
	if err := w.writer.WriteArtifact(ctx, artifact); err != nil {
		return err
	}
	w.count++
	return nil
}

func (r *SourceResult) recordStageTiming(stage string, started time.Time) {
	if r.StageTimingsMS == nil {
		r.StageTimingsMS = map[string]int64{}
	}
	elapsed := time.Since(started).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	r.StageTimingsMS[stage] = elapsed
}

func recordStageTiming(record stageTimingRecorder, stage string, started time.Time) {
	if record == nil {
		return
	}
	record(stage, started)
}

func beginCanonicalReadSnapshot(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin canonical read snapshot: %w", err)
	}
	if err := pinCanonicalReadSnapshot(ctx, tx); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func pinCanonicalReadSnapshot(ctx context.Context, tx *sql.Tx) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sources`).Scan(&count); err != nil {
		return fmt.Errorf("pin canonical read snapshot: %w", err)
	}
	return nil
}

func extractSourceArtifacts(ctx context.Context, source Source) ([]parity.Artifact, error) {
	switch source.Format {
	case "aiagent_v2":
		return parity.ExtractAIAgentV2Source(ctx, parity.AIAgentV2SourceOptions{
			Root:     source.Location,
			SourceID: source.SourceID,
		})
	case "aiagent_v3":
		return parity.ExtractAIAgentV3Source(ctx, parity.AIAgentV3SourceOptions{
			Root:     source.Location,
			SourceID: source.SourceID,
		})
	case "claude-code":
		return parity.ExtractClaudeCodeSource(ctx, parity.ClaudeCodeSourceOptions{
			Root:     source.Location,
			SourceID: source.SourceID,
		})
	case "codex":
		return parity.ExtractCodexSource(ctx, parity.CodexSourceOptions{
			Root:     source.Location,
			SourceID: source.SourceID,
		})
	case "opencode":
		return parity.ExtractOpencodeSource(ctx, parity.OpencodeSourceOptions{
			DBPath:   source.Location,
			SourceID: source.SourceID,
		})
	default:
		return nil, fmt.Errorf("unsupported source extractor for %q", source.Format)
	}
}

func extractTempCanonicalArtifacts(ctx context.Context, opts Options, logger *slog.Logger, source Source) ([]parity.Artifact, error) {
	workDir, cleanup, err := prepareWorkDir(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	st, err := store.OpenWriter(ctx, filepath.Join(workDir, tempDBName), logger)
	if err != nil {
		return nil, fmt.Errorf("open temp canonical db: %w", err)
	}
	defer func() { _ = st.Close() }()

	scanErr := scanSourceIntoDB(ctx, st.DB(), logger, source)
	artifacts, extractErr := parity.ExtractCanonical(ctx, st.DB())
	if scanErr != nil || extractErr != nil {
		return artifacts, errors.Join(scanErr, extractErr)
	}
	return artifacts, nil
}

func prepareWorkDir(configured string) (string, func(), error) {
	if configured != "" {
		if err := os.MkdirAll(configured, 0o700); err != nil {
			return "", func() {}, fmt.Errorf("create parity work dir: %w", err)
		}
		dir, err := os.MkdirTemp(configured, parityWorkDirPattern)
		if err != nil {
			return "", func() {}, fmt.Errorf("create parity work subdir: %w", err)
		}
		return dir, func() {}, nil
	}
	dir, err := os.MkdirTemp("", parityWorkDirPattern)
	if err != nil {
		return "", func() {}, fmt.Errorf("create parity temp dir: %w", err)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func validateWorkDir(configured string, allowRepoOutput bool) error {
	if configured == "" || allowRepoOutput {
		return nil
	}
	repoRoot, ok := detectRepoRoot()
	if !ok {
		return nil
	}
	workDir, err := resolveWorkDirForRepoCheck(configured)
	if err != nil {
		return fmt.Errorf("resolve parity work dir: %w", err)
	}
	if pathWithin(filepath.Clean(repoRoot), filepath.Clean(workDir)) {
		return fmt.Errorf("--work-dir resolves inside the repository; choose a path outside the repo or pass --allow-repo-output for sanitized fixture work")
	}
	return nil
}

func detectRepoRoot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func resolveWorkDirForRepoCheck(configured string) (string, error) {
	workDir, err := filepath.Abs(configured)
	if err != nil {
		return "", err
	}
	workDir = filepath.Clean(workDir)
	if resolved, err := filepath.EvalSymlinks(workDir); err == nil {
		return filepath.Clean(resolved), nil
	}

	parent := workDir
	var missing []string
	for {
		info, err := os.Stat(parent)
		if err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("%s is not a directory", parent)
			}
			resolvedParent, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolvedParent = filepath.Join(resolvedParent, missing[i])
			}
			return filepath.Clean(resolvedParent), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return workDir, nil
		}
		missing = append(missing, filepath.Base(parent))
		parent = next
	}
}

func pathWithin(root string, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func scanSourceIntoDB(ctx context.Context, db *sql.DB, logger *slog.Logger, source Source) error {
	return scanSourceIntoDBWithCursor(ctx, db, logger, source, nil)
}

func scanSourceIntoDBWithCursor(ctx context.Context, db *sql.DB, logger *slog.Logger, source Source, since canonical.Cursor) error {
	factory, ok := adapters.Get(source.Format)
	if !ok {
		return fmt.Errorf("unknown adapter format %q", source.Format)
	}

	adapterErrs := &adapterErrorCollector{}
	adapter, err := factory(source.Location, canonical.AdapterOptions{
		Logger:   logger.With("source_id", source.SourceID, "adapter", source.Format),
		SourceID: source.SourceID,
		OnError:  adapterErrs.add,
	})
	if err != nil {
		return fmt.Errorf("construct adapter: %w", err)
	}

	ing, err := ingest.New(
		db,
		ingest.WithLogger(logger),
		ingest.WithBatchSize(parityBatchSize),
		ingest.WithBatchInterval(parityBatchInterval),
		ingest.WithResolverInterval(parityResolverInterval),
		ingest.WithSourceFormat(source.SourceID, source.Format),
		ingest.WithLocation(source.SourceID, source.Location),
	)
	if err != nil {
		return fmt.Errorf("construct ingester: %w", err)
	}
	ing.SetDeferReadModels(true)
	if err := ing.Start(ctx); err != nil {
		return fmt.Errorf("start ingester: %w", err)
	}

	events := make(chan canonical.Event, parityEventBuffer)
	if err := ing.Submit(source.SourceID, events); err != nil {
		_ = ing.Stop()
		return fmt.Errorf("submit source to ingester: %w", err)
	}
	scanErr := adapter.Scan(ctx, since, events)
	close(events)
	stopErr := ing.Stop()
	resolveErr := ing.ResolveOrphans(ctx)

	var errs []error
	if scanErr != nil {
		errs = append(errs, fmt.Errorf("adapter scan: %w", scanErr))
	}
	if err := adapterErrs.err(); err != nil {
		errs = append(errs, err)
	}
	if stopErr != nil {
		errs = append(errs, fmt.Errorf("stop ingester: %w", stopErr))
	}
	if resolveErr != nil {
		errs = append(errs, fmt.Errorf("resolve temp canonical links: %w", resolveErr))
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return nil
}

type adapterErrorCollector struct {
	mu   sync.Mutex
	errs []error
}

func (c *adapterErrorCollector) add(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.errs = append(c.errs, err)
	c.mu.Unlock()
}

func (c *adapterErrorCollector) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.errs) == 0 {
		return nil
	}
	return fmt.Errorf("adapter reported parse errors: %w", errors.Join(c.errs...))
}

func aggregateState(sources []SourceResult) parity.ResultState {
	state := parity.StatePass
	for _, source := range sources {
		switch source.State {
		case parity.StateIncomplete:
			return parity.StateIncomplete
		case parity.StateFail:
			state = parity.StateFail
		case parity.StateSampleOnly:
			if state == parity.StatePass {
				state = parity.StateSampleOnly
			}
		}
	}
	return state
}

func capFindings(findings []parity.Finding, maxFindings int) []parity.Finding {
	if maxFindings < 0 {
		maxFindings = 0
	}
	if len(findings) <= maxFindings {
		return append([]parity.Finding(nil), findings...)
	}
	return append([]parity.Finding(nil), findings[:maxFindings]...)
}

func mergeFindingSummaries(left []FindingSummary, right []FindingSummary) []FindingSummary {
	counts := map[findingSummaryKey]int{}
	addFindingSummaryCounts(counts, left)
	addFindingSummaryCounts(counts, right)
	out := make([]FindingSummary, 0, len(counts))
	for key, count := range counts {
		out = append(out, FindingSummary{
			Severity: key.severity,
			Code:     key.code,
			Class:    key.class,
			Count:    count,
		})
	}
	sort.Slice(out, func(i int, j int) bool {
		return findingSummarySortKey(out[i]) < findingSummarySortKey(out[j])
	})
	return out
}

func addFindingSummaryCounts(counts map[findingSummaryKey]int, summaries []FindingSummary) {
	for _, summary := range summaries {
		key := findingSummaryKey{
			severity: summary.Severity,
			code:     summary.Code,
			class:    summary.Class,
		}
		counts[key] += summary.Count
	}
}

type findingSummaryKey struct {
	severity parity.Severity
	code     parity.FindingCode
	class    parity.ArtifactClass
}

func findingSummarySortKey(summary FindingSummary) string {
	return string(summary.Severity) + "\x00" + string(summary.Code) + "\x00" + string(summary.Class)
}

func checkLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (r SourceResult) withError(err error) SourceResult {
	r.State = parity.StateIncomplete
	if err != nil {
		r.Errors = append(r.Errors, err.Error())
	}
	return r
}

func incompleteSourceResult(source Source, err error) SourceResult {
	return SourceResult{
		Adapter:  source.Format,
		SourceID: source.SourceID,
		Location: source.Location,
		State:    parity.StateIncomplete,
	}.withError(err)
}
