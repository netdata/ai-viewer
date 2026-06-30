package paritycheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/netdata/ai-viewer/internal/adapters/opencode"
	"github.com/netdata/ai-viewer/internal/canonical"
	"github.com/netdata/ai-viewer/internal/ingest"
	"github.com/netdata/ai-viewer/internal/parity"
	"github.com/netdata/ai-viewer/internal/store"
)

func writeSampledOpencodeTempCanonicalArtifactsWithTimings(
	ctx context.Context,
	opts Options,
	logger *slog.Logger,
	source Source,
	sampled []parity.Artifact,
	filter parity.ArtifactKeyFilter,
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

	sessionIDs := sampledNativeSessionIDs(sampled)
	scanStarted := time.Now()
	scanErr := scanOpencodeSessionsIntoDB(ctx, st.DB(), logger, source, sessionIDs)
	recordStageTiming(record, stageScanTempCanonicalDB, scanStarted)
	extractStarted := time.Now()
	count, extractErr := writeExistingCanonicalArtifactsFiltered(ctx, st.DB(), source, canonicalSnapshotHooks{}, filter, writer)
	recordStageTiming(record, stageExtractCanonicalRows, extractStarted)
	if scanErr != nil || extractErr != nil {
		return count, errors.Join(scanErr, extractErr)
	}
	return count, nil
}

func sampledNativeSessionIDs(sampled []parity.Artifact) []string {
	seen := make(map[string]struct{}, len(sampled))
	out := make([]string, 0, len(sampled))
	for _, artifact := range sampled {
		if artifact.NativeSessionID == "" || artifact.Availability == parity.AvailabilitySourceCorrupt {
			continue
		}
		if _, ok := seen[artifact.NativeSessionID]; ok {
			continue
		}
		seen[artifact.NativeSessionID] = struct{}{}
		out = append(out, artifact.NativeSessionID)
	}
	return out
}

func scanOpencodeSessionsIntoDB(ctx context.Context, db *sql.DB, logger *slog.Logger, source Source, sessionIDs []string) error {
	adapterErrs := &adapterErrorCollector{}
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
	if err := ing.Start(ctx); err != nil {
		return fmt.Errorf("start ingester: %w", err)
	}

	events := make(chan canonical.Event, parityEventBuffer)
	if err := ing.Submit(source.SourceID, events); err != nil {
		_ = ing.Stop()
		return fmt.Errorf("submit source to ingester: %w", err)
	}
	scanErr := opencode.ScanSessions(ctx, source.Location, source.SourceID, sessionIDs, events, opencode.ScanSessionsOptions{
		Logger:  logger.With("source_id", source.SourceID, "adapter", source.Format),
		OnError: adapterErrs.add,
	})
	close(events)
	stopErr := ing.Stop()
	resolveErr := ing.ResolveOrphans(ctx)

	var errs []error
	if scanErr != nil {
		errs = append(errs, fmt.Errorf("adapter scan selected sessions: %w", scanErr))
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
	return errors.Join(errs...)
}
