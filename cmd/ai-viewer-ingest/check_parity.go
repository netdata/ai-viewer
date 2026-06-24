package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/netdata/ai-viewer/internal/parity"
	"github.com/netdata/ai-viewer/internal/paritycheck"
)

type checkParityConfig struct {
	dbPath                 string
	workDir                string
	resumePath             string
	sources                []string
	timeout                time.Duration
	changedSince           time.Duration
	changedSinceCursorPath string
	maxFindings            int
	sample                 int
	concurrency            int
	json                   bool
	debugIDs               bool
	allowRepoOutput        bool
	logLevel               string
	logFormat              string
}

const (
	defaultCheckParityTimeout     = 30 * time.Minute
	defaultCheckParityMaxFindings = 200
	defaultCheckParityConcurrency = 1
	changedSinceCursorPrefix      = "@"
)

var (
	checkParityNativeRowTokenRE    = regexp.MustCompile(`\brow\s+("[^"]+"|[^\s:;,)\]]+)`)
	checkParityNativeQuotedTokenRE = regexp.MustCompile(`\b(session|session_input|part)\s+"([^"]+)"`)
)

func runCheckParity(args []string, stdout, stderr *os.File) int {
	cfg, exitCode, ok := parseCheckParityFlags(args, stderr)
	if !ok {
		return exitCode
	}
	logger, err := newLogger(cfg.logLevel, cfg.logFormat, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest check-parity: %v\n", err)
		return 2
	}
	sources, err := checkParitySources(cfg.sources)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest check-parity: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	result, err := paritycheck.CheckSources(ctx, paritycheck.Options{
		DBPath:                 cfg.dbPath,
		WorkDir:                cfg.workDir,
		Sources:                sources,
		Logger:                 logger,
		MaxFindings:            cfg.maxFindings,
		SampleSize:             cfg.sample,
		Concurrency:            cfg.concurrency,
		ChangedSinceCutoffUS:   changedSinceCutoffUS(cfg.changedSince),
		ChangedSinceCursorPath: cfg.changedSinceCursorPath,
		ResumePath:             cfg.resumePath,
		AllowRepoOutput:        cfg.allowRepoOutput,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest check-parity: %v\n", err)
		return 2
	}
	output := result
	if !cfg.debugIDs {
		output = redactCheckParityResult(result)
	}
	if err := writeCheckParityResult(stdout, output, cfg.json); err != nil {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest check-parity: write result: %v\n", err)
		return 1
	}
	if result.State == parity.StatePass {
		return 0
	}
	return 1
}

func parseCheckParityFlags(args []string, stderr *os.File) (checkParityConfig, int, bool) {
	fs := flag.NewFlagSet("check-parity", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dbPath := fs.String("db", "", "optional existing canonical SQLite DB to check read-only")
	workDir := fs.String("work-dir", "", "optional work directory for temporary fixture-mode DBs")
	resumePath := fs.String("resume", "", "source-level resume cursor file for interrupted full scans")
	timeoutRaw := fs.String("timeout", defaultCheckParityTimeout.String(), "maximum duration for the whole parity run; 0s expires immediately for deterministic timeout tests")
	changedSinceRaw := fs.String("changed-since", "", "diagnostic source-level filter: duration uses source_progress.updated_at with --db; @cursor-file compares source snapshots; never returns PASS full parity")
	maxFindings := fs.Int("max-findings", defaultCheckParityMaxFindings, "maximum detailed findings to emit per source and at top level; 0 emits summaries only")
	sample := fs.Int("sample", 0, "diagnostic source artifact sample size; 0 runs full parity; sampled runs return SAMPLE ONLY")
	concurrency := fs.Int("concurrency", defaultCheckParityConcurrency, "maximum number of top-level sources to check concurrently")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	debugIDs := fs.Bool("debug-ids", false, "preserve raw source paths and native ids in output")
	allowRepoOutput := fs.Bool("allow-repo-output", false, "allow --work-dir inside the repository tree for sanitized fixture work")
	logLevel := fs.String("log-level", "warn", "log level (debug|info|warn|error)")
	logFormat := fs.String("log-format", "text", "log format (json|text)")
	sources := newRepeatableFlag()
	fs.Var(sources, "source", "source in the form <format>:<location>; may be repeated")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: ai-viewer-ingest check-parity --source <format:location> [--db <path>] [--json]\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return checkParityConfig{}, 0, false
		}
		return checkParityConfig{}, 2, false
	}
	if len(sources.values()) == 0 {
		_, _ = fmt.Fprintln(stderr, "ai-viewer-ingest check-parity: at least one --source is required")
		return checkParityConfig{}, 2, false
	}
	timeout, err := time.ParseDuration(*timeoutRaw)
	if err != nil || timeout < 0 {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest check-parity: invalid --timeout %q\n", *timeoutRaw)
		return checkParityConfig{}, 2, false
	}
	var changedSince time.Duration
	var changedSinceCursorPath string
	if *changedSinceRaw != "" {
		if strings.HasPrefix(*changedSinceRaw, changedSinceCursorPrefix) {
			changedSinceCursorPath = strings.TrimPrefix(*changedSinceRaw, changedSinceCursorPrefix)
			if changedSinceCursorPath == "" {
				_, _ = fmt.Fprintln(stderr, "ai-viewer-ingest check-parity: invalid --changed-since cursor")
				return checkParityConfig{}, 2, false
			}
		} else {
			if *dbPath == "" {
				_, _ = fmt.Fprintln(stderr, "ai-viewer-ingest check-parity: --changed-since requires --db")
				return checkParityConfig{}, 2, false
			}
			changedSince, err = time.ParseDuration(*changedSinceRaw)
			if err != nil || changedSince <= 0 {
				_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest check-parity: invalid --changed-since %q\n", *changedSinceRaw)
				return checkParityConfig{}, 2, false
			}
		}
	}
	if *maxFindings < 0 {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest check-parity: invalid --max-findings %d\n", *maxFindings)
		return checkParityConfig{}, 2, false
	}
	if *sample < 0 {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest check-parity: invalid --sample %d\n", *sample)
		return checkParityConfig{}, 2, false
	}
	if *resumePath != "" && *sample > 0 {
		_, _ = fmt.Fprintln(stderr, "ai-viewer-ingest check-parity: --resume cannot be combined with --sample")
		return checkParityConfig{}, 2, false
	}
	if *resumePath != "" && (changedSince > 0 || changedSinceCursorPath != "") {
		_, _ = fmt.Fprintln(stderr, "ai-viewer-ingest check-parity: --resume cannot be combined with --changed-since")
		return checkParityConfig{}, 2, false
	}
	if *concurrency <= 0 {
		_, _ = fmt.Fprintf(stderr, "ai-viewer-ingest check-parity: invalid --concurrency %d\n", *concurrency)
		return checkParityConfig{}, 2, false
	}
	return checkParityConfig{
		dbPath:                 *dbPath,
		workDir:                *workDir,
		resumePath:             *resumePath,
		sources:                sources.values(),
		timeout:                timeout,
		changedSince:           changedSince,
		changedSinceCursorPath: changedSinceCursorPath,
		maxFindings:            *maxFindings,
		sample:                 *sample,
		concurrency:            *concurrency,
		json:                   *jsonOut,
		debugIDs:               *debugIDs,
		allowRepoOutput:        *allowRepoOutput,
		logLevel:               *logLevel,
		logFormat:              *logFormat,
	}, 0, true
}

func changedSinceCutoffUS(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	return time.Now().UTC().Add(-duration).UnixMicro()
}

func checkParitySources(raw []string) ([]paritycheck.Source, error) {
	out := make([]paritycheck.Source, 0, len(raw))
	for _, item := range raw {
		format, location, err := parseSourceFlag(item)
		if err != nil {
			return nil, err
		}
		out = append(out, paritycheck.Source{
			Format:   format,
			Location: location,
			SourceID: format + ":" + location,
		})
	}
	return out, nil
}

func writeCheckParityResult(stdout *os.File, result paritycheck.CheckResult, jsonOut bool) error {
	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	if err := writeCheckParityHumanSummary(stdout, result); err != nil {
		return err
	}
	return nil
}

func writeCheckParityHumanSummary(stdout *os.File, result paritycheck.CheckResult) error {
	if _, err := fmt.Fprintf(stdout, "%s findings=%d", result.State, result.TotalFindings); err != nil {
		return err
	}
	if summary := formatCheckParityFindingSummary(result.FindingSummary); summary != "" {
		if _, err := fmt.Fprintf(stdout, " summary=%s", summary); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(stdout); err != nil {
		return err
	}
	for _, source := range result.Sources {
		if _, err := fmt.Fprintf(stdout, "%s adapter=%s source=%s source_artifacts=%d canonical_artifacts=%d findings=%d errors=%d",
			source.State, source.Adapter, source.SourceID, source.SourceArtifacts, source.CanonicalArtifacts, source.TotalFindings, len(source.Errors)); err != nil {
			return err
		}
		if summary := formatCheckParityFindingSummary(source.FindingSummary); summary != "" {
			if _, err := fmt.Fprintf(stdout, " summary=%s", summary); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return err
		}
	}
	return nil
}

func formatCheckParityFindingSummary(summary []paritycheck.FindingSummary) string {
	if len(summary) == 0 {
		return ""
	}
	parts := make([]string, 0, len(summary))
	for _, item := range summary {
		parts = append(parts, fmt.Sprintf("%s:%s/%s=%d", item.Severity, item.Code, item.Class, item.Count))
	}
	return strings.Join(parts, ",")
}

func redactCheckParityResult(result paritycheck.CheckResult) paritycheck.CheckResult {
	redactions := checkParityRedactions{}
	for _, source := range result.Sources {
		redactions.addRaw(source.SourceID, redactSourceID(source.Adapter, source.SourceID))
		redactions.addToken("location", source.Location)
		redactions.addFindings(source.Findings)
	}
	redactions.addFindings(result.Findings)

	redacted := result
	redacted.Sources = make([]paritycheck.SourceResult, len(result.Sources))
	for i, source := range result.Sources {
		src := source
		src.SourceID = redactions.lookup(src.SourceID)
		src.Location = redactions.lookup(src.Location)
		src.Findings = redactFindings(src.Findings, redactions)
		src.Errors = redactCheckParityErrors(src.Errors, redactions)
		redacted.Sources[i] = src
	}
	redacted.Findings = redactFindings(result.Findings, redactions)
	return redacted
}

type checkParityRedactions map[string]string

type checkParityRedactionPair struct {
	raw         string
	replacement string
}

func (r checkParityRedactions) addRaw(raw string, replacement string) {
	if raw == "" || replacement == "" {
		return
	}
	if _, ok := r[raw]; !ok {
		r[raw] = replacement
	}
}

func (r checkParityRedactions) addToken(kind string, raw string) {
	if raw == "" {
		return
	}
	r.addRaw(raw, redactToken(kind, raw))
}

func (r checkParityRedactions) addFindings(findings []parity.Finding) {
	for _, finding := range findings {
		r.addToken("session", finding.NativeSessionID)
		r.addToken("artifact", finding.NativeArtifactID)
	}
}

func (r checkParityRedactions) lookup(raw string) string {
	if raw == "" {
		return ""
	}
	if replacement, ok := r[raw]; ok {
		return replacement
	}
	return raw
}

func (r checkParityRedactions) orderedPairs() []checkParityRedactionPair {
	pairs := make([]checkParityRedactionPair, 0, len(r))
	for raw, replacement := range r {
		pairs = append(pairs, checkParityRedactionPair{
			raw:         raw,
			replacement: replacement,
		})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if len(pairs[i].raw) != len(pairs[j].raw) {
			return len(pairs[i].raw) > len(pairs[j].raw)
		}
		return pairs[i].raw < pairs[j].raw
	})
	return pairs
}

func redactFindings(findings []parity.Finding, redactions checkParityRedactions) []parity.Finding {
	out := make([]parity.Finding, len(findings))
	for i, finding := range findings {
		f := finding
		if redacted, ok := redactions[f.SourceID]; ok {
			f.SourceID = redacted
		} else if f.SourceID != "" {
			f.SourceID = redactSourceID(f.Adapter, f.SourceID)
		}
		f.NativeSessionID = redactToken("session", f.NativeSessionID)
		f.NativeArtifactID = redactToken("artifact", f.NativeArtifactID)
		out[i] = f
	}
	return out
}

func redactCheckParityErrors(errors []string, redactions checkParityRedactions) []string {
	out := make([]string, len(errors))
	for i, msg := range errors {
		redacted := msg
		for _, pair := range redactions.orderedPairs() {
			redacted = strings.ReplaceAll(redacted, pair.raw, pair.replacement)
		}
		redacted = redactNativeTokensInCheckParityError(redacted)
		out[i] = redacted
	}
	return out
}

func redactNativeTokensInCheckParityError(msg string) string {
	redacted := checkParityNativeRowTokenRE.ReplaceAllStringFunc(msg, func(match string) string {
		parts := checkParityNativeRowTokenRE.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		raw := parts[1]
		quoted := strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`)
		token := strings.Trim(raw, `"`)
		if token == "" || strings.HasPrefix(token, "<redacted-") {
			return match
		}
		redactedToken := redactToken("artifact", token)
		if quoted {
			redactedToken = `"` + redactedToken + `"`
		}
		return "row " + redactedToken
	})
	return checkParityNativeQuotedTokenRE.ReplaceAllStringFunc(redacted, func(match string) string {
		parts := checkParityNativeQuotedTokenRE.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		label := parts[1]
		token := parts[2]
		if token == "" || strings.HasPrefix(token, "<redacted-") {
			return match
		}
		kind := "artifact"
		if label == "session" {
			kind = "session"
		}
		return label + ` "` + redactToken(kind, token) + `"`
	})
}

func redactSourceID(adapter string, sourceID string) string {
	if adapter == "" {
		return redactToken("source", sourceID)
	}
	return adapter + ":" + redactToken("source", sourceID)
}

func redactToken(kind string, value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("<redacted-%s:%x>", kind, sum[:6])
}
