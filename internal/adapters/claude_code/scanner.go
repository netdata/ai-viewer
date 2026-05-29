package claude_code

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// transcriptExt is the required extension for claude-code transcripts.
const transcriptExt = ".jsonl"

// metaExt is the suffix of a subagent metadata sidecar.
const metaExt = ".meta.json"

// subagentsDir is the directory under a session dir holding sidechains.
const subagentsDir = "subagents"

// scanBufferMax bounds a single transcript line. Real claude-code lines can
// be very large (operator-pasted content, big tool results); 8 MB is ample
// while bounding pathological allocations.
const scanBufferMax = 8 * 1024 * 1024

// progressEveryEvents bounds how frequently SourceProgress is emitted by
// record count (spec §7 "every N lines or T ms").
const progressEveryEvents = 200

// progressEveryDuration bounds SourceProgress emission by wall-clock.
const progressEveryDuration = 5 * time.Second

// transcript describes one transcript file discovered under the root.
type transcript struct {
	// rel is the path relative to the root (the cursor key).
	rel string
	// abs is the absolute path on disk.
	abs string
	// nativeID is the canonical session id for this file.
	nativeID string
	// parentNativeID is empty for main transcripts; the parent sessionId
	// for subagents.
	parentNativeID string
	// kind is root or sub_agent.
	kind canonical.SessionKind
	// sessionDir is the absolute path of the parent <sessionId>/ dir for a
	// subagent (used to locate sibling sidecar metas), or "" for main.
	sessionDir string
}

// discoverTranscripts walks the projects root and returns every transcript
// (main + subagent) sorted by relative path for deterministic replay. The
// main-session sessionId is taken from the file basename; the subagent
// agentId from the agent-<agentId>.jsonl basename and the parent sessionId
// from the enclosing <sessionId>/ dir. Orphan-root sessions (a <sessionId>/
// dir with only subagents/ and no parent .jsonl) are detected here and a
// synthetic root transcript is NOT added — the orphan root session is
// synthesized in scanAll from the union of its children (spec §10.1).
//
// Every discovered transcript path is resolved with filepath.EvalSymlinks and
// verified to stay inside the resolved projects root before it is returned
// (spec §6.1, P2e, security.md §6 "No symlink traversal escape"). A path that
// escapes the root — a planted symlink pointing at /etc/passwd or any location
// outside the source — is refused with a SourceError via onError and skipped;
// it is never opened or read. onError may be nil for callers that do not need
// the diagnostic (it is then treated as a no-op).
func discoverTranscripts(root string, onError func(error)) ([]transcript, error) {
	if onError == nil {
		onError = func(error) {}
	}
	resolvedRoot, rerr := filepath.EvalSymlinks(filepath.Clean(root))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve projects root %s: %w", root, rerr)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read projects root %s: %w", root, err)
	}
	var out []transcript
	for _, projEntry := range entries {
		if !projEntry.IsDir() {
			continue
		}
		projDir := filepath.Join(root, projEntry.Name())
		ts, derr := discoverProject(root, resolvedRoot, projDir, onError)
		if derr != nil {
			return nil, derr
		}
		out = append(out, ts...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// withinSourceRoot reports whether abs resolves (through symlinks) to a path
// inside resolvedRoot (spec §6.1, P2e). The first argument MUST be the
// ALREADY-symlink-resolved projects root: callers resolve the root ONCE (e.g.
// discoverTranscripts / scanAll) and thread it here, so a directory walk does
// not re-resolve the root once per file (P2-perf). On escape it surfaces a
// SourceError via onError and returns false; on a resolve error it likewise
// surfaces and returns false, so a path that cannot be safely resolved is
// skipped rather than read.
func withinSourceRoot(resolvedRoot, abs string, onError func(error)) bool {
	resolved, ok, err := withinResolvedRoot(resolvedRoot, abs)
	if err != nil {
		onError(fmt.Errorf("claude_code: cannot resolve %s for containment; skipping: %w", abs, err))
		return false
	}
	if !ok {
		onError(fmt.Errorf("claude_code: %s resolves to %s outside the projects root; skipping (symlink escape)", abs, resolved))
		return false
	}
	return true
}

// discoverProject enumerates transcripts under a single
// <root>/<sanitized-cwd>/ project directory. resolvedRoot is the
// symlink-resolved projects root used for the per-path containment guard
// (P2e); root is the unresolved root used for cursor-key relativisation.
func discoverProject(root, resolvedRoot, projDir string, onError func(error)) ([]transcript, error) {
	entries, err := os.ReadDir(projDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read project dir %s: %w", projDir, err)
	}
	var out []transcript
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			// A <sessionId>/ session dir: scan its subagents/.
			sub, serr := discoverSessionSubagents(root, resolvedRoot, projDir, name, onError)
			if serr != nil {
				return nil, serr
			}
			out = append(out, sub...)
			continue
		}
		if !strings.HasSuffix(name, transcriptExt) {
			continue
		}
		abs := filepath.Join(projDir, name)
		if !withinSourceRoot(resolvedRoot, abs, onError) {
			continue
		}
		sessionID := strings.TrimSuffix(name, transcriptExt)
		rel, rerr := relPath(root, abs)
		if rerr != nil {
			return nil, rerr
		}
		out = append(out, transcript{
			rel:      rel,
			abs:      abs,
			nativeID: sessionID,
			kind:     canonical.KindRoot,
			// A main transcript's sidecar metas (if any) live under
			// <sessionId>/subagents/. Point sessionDir there so the
			// mapper can recover the toolUseId→agentId map and set
			// ChildSessionNativeID on the parent's Agent ops (spec §8).
			sessionDir: filepath.Join(projDir, sessionID),
		})
	}
	return out, nil
}

// discoverSessionSubagents enumerates the subagent transcripts under
// <projDir>/<sessionId>/subagents/ (recursively, to cover workflow
// subdirs). parentSessionID is the enclosing <sessionId> dir name. Each
// discovered sidechain path is containment-checked (P2e) before it is added.
func discoverSessionSubagents(root, resolvedRoot, projDir, sessionID string, onError func(error)) ([]transcript, error) {
	subDir := filepath.Join(projDir, sessionID, subagentsDir)
	var out []transcript
	walkErr := filepath.WalkDir(subDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, transcriptExt) {
			return nil
		}
		if !strings.HasPrefix(name, "agent-") {
			return nil
		}
		if !withinSourceRoot(resolvedRoot, path, onError) {
			return nil
		}
		agentID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), transcriptExt)
		rel, rerr := relPath(root, path)
		if rerr != nil {
			return rerr
		}
		out = append(out, transcript{
			rel:            rel,
			abs:            path,
			nativeID:       childNativeID(sessionID, agentID),
			parentNativeID: sessionID,
			kind:           canonical.KindSubAgent,
			sessionDir:     filepath.Join(projDir, sessionID),
		})
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return out, fmt.Errorf("walk subagents %s: %w", subDir, walkErr)
	}
	return out, nil
}

// relPath returns abs relative to root with forward slashes, the canonical
// cursor key form.
func relPath(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("relpath %s under %s: %w", abs, root, err)
	}
	return filepath.ToSlash(rel), nil
}

// metaMap is the toolUseId→agentId map recovered from a session dir's
// sidecar .meta.json files, plus the per-agent agentType (the subagent's
// effective AgentName). Keyed lookups are by agentId.
type metaMap struct {
	toolUseToAgent map[string]string
	agentType      map[string]string
}

// readSessionMetas reads every agent-*.meta.json under a session dir's
// subagents/ and returns the toolUseId→agentId map plus per-agent agentType.
// Missing dir / unreadable files yield an empty map (best-effort; the
// structural linkage via path still works without the sidecar). resolvedRoot is
// the ALREADY-symlink-resolved projects root: every meta path is
// containment-checked against it before it is read (spec §6.1, P1.3) without
// re-resolving the root per file. A symlinked .meta.json resolving outside the
// root is refused with a SourceError and skipped, so its (potentially
// sensitive) content is never absorbed into a session's extras.
func readSessionMetas(resolvedRoot, sessionDir string, onError func(error)) metaMap {
	mm := metaMap{toolUseToAgent: map[string]string{}, agentType: map[string]string{}}
	if sessionDir == "" {
		return mm
	}
	subDir := filepath.Join(sessionDir, subagentsDir)
	// Two-phase (mirrors aiagent_v3 list-then-open): collect meta paths in
	// the walk, then read them after the walk returns. Reading inside the
	// WalkDir callback is a TOCTOU-prone pattern (gosec G122); separating the
	// phases keeps the read off the callback path.
	paths := collectMetaPaths(subDir)
	for _, path := range paths {
		// Containment guard returning the RESOLVED path so the read opens it, not
		// the original (P2.4a, no TOCTOU). A path that escapes the root surfaces a
		// SourceError via withinSourceRoot's onError and is skipped.
		resolvedPath, ok, rerr := withinResolvedRoot(resolvedRoot, path)
		if rerr != nil {
			onError(fmt.Errorf("claude_code: cannot resolve meta %s for containment; skipping: %w", path, rerr))
			continue
		}
		if !ok {
			onError(fmt.Errorf("claude_code: meta %s resolves outside the projects root; skipping (symlink escape)", path))
			continue
		}
		agentID := strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), metaExt), "agent-")
		raw, readErr := os.ReadFile(resolvedPath) // #nosec G304 G122 -- reading the containment-checked RESOLVED path collected from a filtered walk under the configured read-only projects root
		if readErr != nil {
			// A present-but-unreadable meta silently fails the toolUseId→agentId
			// linkage repair and the child's AgentName; surface it (P2.4b, no
			// silent failure) so /api/health shows it.
			onError(fmt.Errorf("claude_code: read meta %s: %w", path, readErr))
			continue
		}
		var meta struct {
			AgentType string `json:"agentType"`
			ToolUseID string `json:"toolUseId"`
		}
		if jerr := json.Unmarshal(raw, &meta); jerr != nil {
			onError(fmt.Errorf("claude_code: parse meta %s: %w", path, jerr))
			continue
		}
		if meta.AgentType != "" {
			mm.agentType[agentID] = meta.AgentType
		}
		if meta.ToolUseID != "" {
			mm.toolUseToAgent[meta.ToolUseID] = agentID
		}
	}
	return mm
}

// collectMetaPaths walks dir and returns the absolute paths of every
// agent-*.meta.json found, sorted for determinism. A missing dir yields an
// empty slice. The walk records names only — file reads happen after the
// walk returns (see readSessionMetas / metaHashes).
func collectMetaPaths(dir string) []string {
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), metaExt) {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths
}

// metaHashes returns a map of meta-file relative path → sha256 of content
// for every .meta.json under the projects root. Used by the tail/scan loop
// to detect sidecar rewrites (spec §7 step 4). Two-phase like
// readSessionMetas: collect paths, then read off the walk callback. root is the
// UNRESOLVED configured root (the walk source and the cursor-key relativisation
// base, per spec §7); resolvedRoot is the symlink-resolved root used for the
// per-path containment check WITHOUT re-resolving it per file (P2-perf).
// Each meta path is containment-checked before it is read (spec §6.1, P1.3): a
// symlinked .meta.json resolving outside the root is refused (SourceError) and
// skipped.
func metaHashes(root, resolvedRoot string, onError func(error)) (map[string]string, error) {
	out := map[string]string{}
	paths := collectMetaPaths(root)
	for _, path := range paths {
		// Containment guard returning the RESOLVED path so the hash reads it, not
		// the original (P2.4a, no TOCTOU).
		resolvedPath, ok, cerr := withinResolvedRoot(resolvedRoot, path)
		if cerr != nil {
			onError(fmt.Errorf("claude_code: cannot resolve meta %s for containment; skipping: %w", path, cerr))
			continue
		}
		if !ok {
			onError(fmt.Errorf("claude_code: meta %s resolves outside the projects root; skipping (symlink escape)", path))
			continue
		}
		rel, rerr := relPath(root, path)
		if rerr != nil {
			onError(fmt.Errorf("claude_code: relpath meta %s: %w", path, rerr))
			continue
		}
		raw, readErr := os.ReadFile(resolvedPath) // #nosec G304 G122 -- reading the containment-checked RESOLVED path collected from a filtered walk under the configured read-only projects root
		if readErr != nil {
			// A present-but-unreadable meta silently fails the rewrite-detection
			// that drives the late-meta linkage repair; surface it (P2.4b).
			onError(fmt.Errorf("claude_code: read meta %s: %w", path, readErr))
			continue
		}
		out[rel] = hashBytes(raw)
	}
	return out, nil
}

// hashBytes returns the hex-encoded sha256 of b. Shared by the scan-time
// meta-hash walk and the tail-time per-file meta checkpoint.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readTranscript parses one transcript from its cursor offset to EOF, emits
// canonical events, and returns the updated FileCursor and event count. The
// caller supplies the per-session metaMap so the file's mapper can set
// ChildSessionNativeID on Agent ops. Partial trailing lines are held back
// (offset advances only past complete lines) per spec §6.3.
func readTranscript(ctx context.Context, root string, t transcript, sourceID string, mm metaMap, start FileCursor, out chan<- canonical.Event, onError func(error)) (FileCursor, int, *fileMapper, error) {
	// Containment guard on EVERY transcript open (spec §6.1, P1.3): Scan's
	// discovery already checks discovered paths, but the Tail flush path
	// reconstructs a transcript from a relative path (transcriptForRel) WITHOUT
	// a check, so a *.jsonl symlink planted in a watched dir after Tail starts
	// would otherwise be opened. Resolving here makes the guard uniform across
	// Scan and Tail. A refused path surfaces a SourceError (the caller logs the
	// returned error) and the file is skipped — never opened.
	//
	// Open the RESOLVED path, not the original t.abs (P2.4a, no TOCTOU): checking
	// the resolved path then opening the unresolved one leaves a time-of-check/
	// time-of-use window where a symlink swapped between the two reads outside the
	// root. The resolved path is fully symlink-evaluated, so no further swap can
	// redirect it.
	resolvedAbs, ok, cerr := resolveWithinRoot(root, t.abs)
	if cerr != nil {
		return start, 0, nil, fmt.Errorf("claude_code: cannot resolve %s for containment; skipping: %w", t.abs, cerr)
	} else if !ok {
		return start, 0, nil, fmt.Errorf("claude_code: %s resolves outside the projects root; skipping (symlink escape)", t.rel)
	}
	f, err := os.Open(resolvedAbs) // #nosec G304 -- opening the containment-checked RESOLVED path (resolveWithinRoot) from a filtered scan under the configured root
	if err != nil {
		return start, 0, nil, fmt.Errorf("open %s: %w", t.abs, err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return start, 0, nil, fmt.Errorf("stat %s: %w", t.abs, err)
	}
	size := info.Size()
	cur := start

	// Truncation defense (spec §7 step 2): a shrunken file is re-scanned
	// from 0; SQL-layer idempotent upserts absorb any re-emitted rows.
	if cur.Size > 0 && size < cur.Size {
		onError(fmt.Errorf("transcript %s shrank (size=%d, cursor.size=%d); rescanning from 0", t.rel, size, cur.Size))
		cur = FileCursor{}
	}

	emitFrom := cur.Offset
	mapper := newFileMapper(mapperConfig{
		sourceID:       sourceID,
		absPath:        t.abs,
		nativeID:       t.nativeID,
		parentNativeID: t.parentNativeID,
		kind:           t.kind,
		agentName:      agentNameFor(t, mm),
		toolUseToAgent: mm.toolUseToAgent,
		root:           root,
		sessionDir:     t.sessionDir,
	})

	// Even when the file is fully consumed (offset >= size) we must replay the
	// chain from offset 0 with the emit-gate set to the current size (emit
	// NOTHING) so the per-file Agent-op map AND the child's last-record
	// timestamp are reconstructed (spec §8.1, P1.2). A naive early-return with an
	// empty mapper would make a parent's Agent op invisible to Tail's deferral
	// after the Scan→Tail boundary — the child completing later would then never
	// finalize the parent — and would stamp a fully-read child's finalize at
	// ts=0. emitFrom is clamped to >= size below, so already-consumed bytes
	// (all of them, in this case) rebuild state without re-emitting any event.
	if emitFrom > size {
		emitFrom = size
	}

	// Always parse from offset 0 so the per-file turn/op inference counters
	// are rebuilt deterministically — claude-code has no native turn/op
	// numbers, so the only way a resume produces the SAME Seqs (and the same
	// SessionStarted/turn boundaries) as a one-shot pass is to replay the
	// chain from the start. Emission is gated to records whose line begins at
	// or after the resume offset (emitFrom), so a resume yields ZERO duplicate
	// and ZERO gap (acceptance #6). The byte offset is the durable resume key;
	// the cheap re-parse of already-consumed bytes is discarded, not emitted.
	emitted, advanced, perr := streamLines(ctx, f, emitFrom, t, sourceID, mapper, out, onError)
	if perr != nil {
		return cur, emitted, mapper, perr
	}
	cur.Offset = advanced
	cur.Size = size
	// The file is fully read when the offset reached EOF (no parked partial
	// line). A parked partial line leaves advanced < size, meaning the child
	// is still being written and its parent Agent op stays running (§8.1).
	mapper.fullyRead = advanced >= size
	return cur, emitted, mapper, nil
}

// agentNameFor returns the AgentName a transcript's session should start
// with: the subagent's agentType from .meta.json (sub_agent), or "" for a
// main session (filled later from custom/ai title records).
func agentNameFor(t transcript, mm metaMap) string {
	if t.kind != canonical.KindSubAgent {
		return ""
	}
	agentID := agentIDFromNative(t.nativeID)
	return mm.agentType[agentID]
}

// agentIDFromNative extracts the agentId from a synthetic subagent NativeID
// ("<parent>:agent:<agentId>").
func agentIDFromNative(nativeID string) string {
	if i := strings.LastIndex(nativeID, ":agent:"); i >= 0 {
		return nativeID[i+len(":agent:"):]
	}
	return ""
}

// streamLines reads '\n'-terminated JSON records from r (positioned at
// offset 0), mapping each via the file's mapper to rebuild turn/op inference
// state deterministically. Events are emitted ONLY for records whose line
// begins at or after emitFrom, so a resume replays prior bytes to rebuild
// counters but emits nothing already seen (zero dup, zero gap). Returns the
// emitted-event count and the absolute offset just past the last complete
// line consumed. Parse errors before emitFrom are not re-surfaced (they were
// reported on the first pass).
func streamLines(ctx context.Context, r io.Reader, emitFrom int64, t transcript, sourceID string, mapper *fileMapper, out chan<- canonical.Event, onError func(error)) (int, int64, error) {
	br := bufio.NewReaderSize(r, 64*1024)
	emitted := 0
	off := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return emitted, off, err
		}
		line, consumed, err := readOneLine(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return emitted, off, nil
			}
			if errors.Is(err, errLineTooLong) {
				// Surface exactly one SourceError for the oversized line, advance
				// past the bytes drained up to and including its terminating
				// newline, and CONTINUE reading subsequent records (spec §6.3,
				// P2.5). Jumping to EOF here would silently discard every later
				// valid record in the file. If the oversized line had no trailing
				// newline (drained to EOF), consumed covers the rest of the file
				// and the next read returns io.EOF.
				emit := off >= emitFrom
				if emit {
					onError(fmt.Errorf("transcript %s @%d: line exceeds %d bytes; skipping", t.rel, off, scanBufferMax))
				}
				// An oversized line is the PHYSICAL last record if it is the file's
				// trailing line, so the §485 completion marker must reflect it: clear
				// the assistant-text flag (an unbuffered line is never an
				// assistant-text record) so a child ending in [assistant{text},
				// <oversized line>] does NOT finalize its parent Agent op (P1.5a).
				// This mirrors the parse-error and skipped-no-op continues — every
				// physical-line-consuming path leaves the flag reflecting THAT line;
				// the only non-consuming exit is the io.EOF return above.
				mapper.lastRecordAssistantText = false
				mapper.lastRecordEmitted = emit
				off += consumed
				continue
			}
			return emitted, off, fmt.Errorf("read %s @%d: %w", t.rel, off, err)
		}
		if len(line) == 0 {
			return emitted, off, nil
		}
		recBytes := line[:len(line)-1]
		lineStart := off
		off += int64(len(line))
		emit := lineStart >= emitFrom

		rec, skip, perr := parseLine(recBytes)
		if perr != nil {
			if emit && shouldSurfaceParseError(mapper, perr) {
				onError(fmt.Errorf("transcript %s @%d: %w", t.rel, lineStart, perr))
			}
			// A malformed line is the PHYSICAL last record if it is the file's
			// trailing line, so the §485 completion marker must reflect it: clear
			// the assistant-text flag (a parse error is never an assistant-text
			// record) so a child ending in [assistant{text}, <malformed>] does NOT
			// finalize its parent Agent op (P1.4). lastRecordEmitted tracks whether
			// this physical line was newly read, mirroring the mapped-record path.
			mapper.lastRecordAssistantText = false
			mapper.lastRecordEmitted = emit
			continue
		}
		if skip {
			// A skipped known-no-op record (e.g. a trailing summary/task-summary)
			// is also a PHYSICAL record: clear the completion flag so a child whose
			// last physical line is [assistant{text}, <skipped no-op>] is NOT
			// complete (P1.4). Without this the flag would stay stale-true from the
			// preceding assistant-text record and wrongly finalize the parent.
			mapper.lastRecordAssistantText = false
			mapper.lastRecordEmitted = emit
			continue
		}
		// mapRecord always runs so the turn/op inference counters advance
		// during a resume replay; only the events past emitFrom are sent. It also
		// sets lastRecordAssistantText for THIS record before any error return
		// (mapper.go), so the completion flag already reflects this physical line.
		events, mErr := mapper.mapRecord(rec)
		if mErr != nil {
			if emit {
				onError(fmt.Errorf("transcript %s @%d: map: %w", t.rel, lineStart, mErr))
			}
			// Keep lastRecordEmitted in lockstep with every other physical-line
			// path (the map-error record is still the physical last line if it is
			// the file's trailing record); lastRecordAssistantText was already set
			// by mapRecord above.
			mapper.lastRecordEmitted = emit
			continue
		}
		// Record whether this (now the most recent) record was newly read this
		// pass, so the Agent-op finalize can be gated on the terminal
		// assistant-text record being NEWLY read (§8.1): a replay re-reads the
		// terminal record below emitFrom (emit=false) and so does not re-mark the
		// child completed.
		mapper.lastRecordEmitted = emit
		if !emit {
			continue
		}
		for _, ev := range events {
			select {
			case <-ctx.Done():
				return emitted, off, ctx.Err()
			case out <- ev:
				emitted++
			}
		}
	}
}

// shouldSurfaceParseError reports whether a per-line parse error should be
// forwarded to onError. Unknown-`type` errors are deduped to one per distinct
// variant per file (spec §3.12, acceptance #2) via the mapper's seen-set; all
// other parse errors (malformed JSON, missing type, decode failures) surface
// every time because they describe a distinct broken line, not a repeated
// known-unknown variant.
func shouldSurfaceParseError(mapper *fileMapper, perr error) bool {
	var ute *unknownTypeError
	if errors.As(perr, &ute) {
		return mapper.firstUnknownType(ute.Type)
	}
	return true
}

// errLineTooLong signals that a single transcript line exceeded
// scanBufferMax. The caller surfaces it via onError and skips to EOF.
var errLineTooLong = errors.New("claude_code: line exceeds scan buffer")

// readOneLine reads one '\n'-terminated record from br, returning the line
// WITH the trailing '\n' so callers can advance offset by len(). Returns
// io.EOF (with consumed=0) when no complete line is available — it never
// returns a partial trailing line, implementing the spec §6.3 hold-back
// invariant. On errLineTooLong it returns the number of bytes drained up to
// AND including the next '\n' (or to EOF when the oversized line is the file's
// trailing bytes) so the caller can advance past the skipped line and continue
// (spec §6.3, P2.5). consumed is meaningful only for the errLineTooLong and
// nil-error cases; it is 0 for io.EOF and other errors.
func readOneLine(br *bufio.Reader) ([]byte, int64, error) {
	buf := make([]byte, 0, 256)
	for {
		chunk, err := br.ReadSlice('\n')
		if err == nil {
			buf = append(buf, chunk...)
			if len(buf) > scanBufferMax {
				return nil, int64(len(buf)), errLineTooLong
			}
			return buf, int64(len(buf)), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			buf = append(buf, chunk...)
			if len(buf) > scanBufferMax {
				// Drain the rest of the oversized line and report the total
				// bytes consumed so the caller advances past it and continues.
				drained, drainErr := drainToNewline(br)
				if drainErr != nil && !errors.Is(drainErr, io.EOF) {
					return nil, 0, drainErr
				}
				return nil, int64(len(buf)) + drained, errLineTooLong
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			// Partial line at EOF: do not return it (hold-back).
			return nil, 0, io.EOF
		}
		return nil, 0, err
	}
}

// drainToNewline reads and discards bytes from br up to and including the next
// '\n', returning the number of bytes consumed. On io.EOF (the oversized line
// runs to the end of the file with no trailing newline) it returns the bytes
// consumed so far together with io.EOF so the caller can advance the offset to
// EOF; the next read then reports io.EOF cleanly.
func drainToNewline(br *bufio.Reader) (int64, error) {
	var consumed int64
	for {
		chunk, err := br.ReadSlice('\n')
		consumed += int64(len(chunk))
		if err == nil {
			return consumed, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return consumed, err
	}
}

// scanAll walks the projects root and reads every transcript from its
// cursor offset to EOF, emitting events and periodic SourceProgress.
// Orphan-root sessions (a <sessionId>/ dir with only subagents/ and no
// parent .jsonl) get a synthetic root SessionStartedEvent so the children
// have a parent to attach to (spec §10.1). Returns the final cursor.
func scanAll(ctx context.Context, root, sourceID string, start Cursor, out chan<- canonical.Event, onError func(error)) (Cursor, error) {
	transcripts, err := discoverTranscripts(root, onError)
	if err != nil {
		return start, err
	}
	cur := start
	if cur.Files == nil {
		cur = newCursor()
	}

	// Pre-resolve the projects root ONCE so the per-file meta containment checks
	// (readSessionMetas, metaHashes) do not re-run EvalSymlinks on the root for
	// every file (P2-perf), mirroring discoverTranscripts. A resolve failure
	// here is non-fatal: fall back to the unresolved root for containment (the
	// transcripts were already discovered, so the root exists; a degenerate
	// resolve only loses the perf optimisation, not correctness).
	resolvedRoot := root
	if rr, rrErr := filepath.EvalSymlinks(filepath.Clean(root)); rrErr == nil {
		resolvedRoot = rr
	}

	if perr := emitOrphanRoots(ctx, resolvedRoot, sourceID, transcripts, out); perr != nil {
		return cur, perr
	}

	// Group subagent transcripts by their parent session dir so each file's
	// metaMap is read once per session dir.
	metaCache := map[string]metaMap{}
	emittedSinceProgress := 0
	lastProgress := time.Now()

	// Agent-op deferral state (spec §8.1). pending maps a child session native
	// id → its parent's Agent op. completed maps a child native id → completion
	// state once its sidechain is fully read AND terminates in a newly-read
	// assistant-text marker (§485). After the walk, every completed child whose
	// parent op is known is finalized exactly once (finalized set guards
	// re-emit). Seed completed from the persisted cursor's parked set so a child
	// that completed before its parent op was known survives a restart in which
	// the parent appears during THIS Scan (P2.4d), and seed finalized from the
	// cursor so a child already finalized in a prior lifetime is not re-finalized
	// when re-read here (P2.5c). Restore from `start` (the caller's persisted
	// cursor) so the state is not lost if `cur` was reset to a fresh cursor above
	// (start.Files == nil). Restore finalized FIRST so the parked restore's guard
	// sees it.
	def := newTailDeferral()
	def.restoreFinalized(start.finalizedSet())
	def.restoreParked(start.Parked)

	for _, t := range transcripts {
		if err := ctx.Err(); err != nil {
			return cur, err
		}
		mm := metaMap{}
		if t.sessionDir != "" {
			cached, ok := metaCache[t.sessionDir]
			if !ok {
				cached = readSessionMetas(resolvedRoot, t.sessionDir, onError)
				metaCache[t.sessionDir] = cached
			}
			mm = cached
		}
		fc := cur.fileCursor(t.rel)
		updated, n, mapper, rerr := readTranscript(ctx, root, t, sourceID, mm, fc, out, onError)
		if rerr != nil {
			if errors.Is(rerr, context.Canceled) || errors.Is(rerr, context.DeadlineExceeded) {
				return cur, rerr
			}
			onError(rerr)
			continue
		}
		cur = cur.withFile(t.rel, updated)
		collectAgentDeferral(mapper, t, def.pending, def.completed)
		emittedSinceProgress += n
		if emittedSinceProgress >= progressEveryEvents || time.Since(lastProgress) >= progressEveryDuration {
			// Checkpoint parked completions AND the finalized set so a Scan
			// interrupted mid-walk still persists children awaiting their parent op
			// (P2.4d) and any restored already-finalized guard (P2.5c).
			cur = cur.withParked(def.parkedSnapshot())
			cur = cur.withFinalized(def.finalizedSnapshot())
			if perr := emitProgress(ctx, sourceID, cur, out); perr != nil {
				return cur, perr
			}
			emittedSinceProgress = 0
			lastProgress = time.Now()
		}
	}

	// Finalize parent Agent ops whose child sidechain completed (spec §8.1). A
	// child terminated by a non-assistant-text record, or still in-flight, is
	// not in `completed` and leaves its parent op running. Deterministic order.
	if perr := pairCompletedFinalizations(ctx, sourceID, def, out); perr != nil {
		return cur, perr
	}

	// Refresh meta hashes so the cursor records the sidecar state observed.
	if hashes, herr := metaHashes(root, resolvedRoot, onError); herr == nil {
		for rel, h := range hashes {
			cur = cur.withMetaSeen(rel, h)
		}
	}

	// Persist the parked completions that survived pairing (children still
	// awaiting their parent op) so a restart can restore them (P2.4d), and the
	// finalized set so a child finalized during THIS Scan is not re-finalized by
	// the subsequent Tail catch-up or a late-meta re-read (P2.5c). Finalized
	// children were dropped from def.completed and added to def.finalized by
	// pairCompletedFinalizations, so the parked snapshot excludes them and the
	// finalized snapshot includes them.
	cur = cur.withParked(def.parkedSnapshot())
	cur = cur.withFinalized(def.finalizedSnapshot())

	if err := emitProgress(ctx, sourceID, cur, out); err != nil {
		return cur, err
	}
	return cur, nil
}

// agentOpFinalize captures the parent session and op location of a deferred
// Agent op (spec §8.1).
type agentOpFinalize struct {
	parentNativeID string
	ref            agentOpRef
}

// collectAgentDeferral folds one transcript's mapper state into the cross-file
// Agent-op deferral maps (spec §8.1). A parent transcript contributes its Agent
// ops (keyed by child native id) into `pending` — rebuilt on every read,
// including the emit-suppressed replay, so a parent observed during Scan is
// reachable in Tail.
//
// The subagent → `completed` fold is BIDIRECTIONAL (P1.5b):
//   - ADD: a subagent fully read AND ending in an assistant-text completion
//     marker (§485) that was NEWLY read this pass (lastRecordEmitted) is added to
//     `completed`. The emit gate means a catch-up/replay over an already-consumed
//     child does not re-mark it (no double finalize).
//   - RETRACT: a subagent re-read that is NOT currently complete
//     (!(fullyRead && lastRecordAssistantText) — e.g. it grew a trailing
//     tool_use/user record after a prior pass parked it complete) is DELETED from
//     `completed`. Without this, a child that completed and parked (parent op not
//     yet known) but then grew a non-text terminal record before the parent op
//     appeared would keep its stale park and wrongly finalize the parent.
//
// The emit gate is on the ADD branch ONLY: a pure replay of an already-complete
// child (fully read, terminal assistant-text, but read below the resume offset so
// lastRecordEmitted is false) is neither re-added nor retracted (it IS still
// complete), preserving the no-double-finalize property.
func collectAgentDeferral(mapper *fileMapper, t transcript, pending map[string]agentOpFinalize, completed map[string]completionState) {
	if mapper == nil {
		return
	}
	for childID, ref := range mapper.agentOps {
		pending[childID] = agentOpFinalize{parentNativeID: mapper.nativeID, ref: ref}
	}
	if t.kind != canonical.KindSubAgent {
		return
	}
	currentlyComplete := mapper.fullyRead && mapper.lastRecordAssistantText
	switch {
	case currentlyComplete && mapper.lastRecordEmitted:
		completed[t.nativeID] = completionState{tsUs: mapper.lastAssistantTextTsUs}
	case !currentlyComplete:
		// Re-read child that is no longer complete: retract any stale park so it
		// cannot finalize the parent. A no-op when the child was never parked.
		delete(completed, t.nativeID)
	}
}

// emitOrphanRoots emits a synthetic root SessionStartedEvent for every
// parent sessionId that has subagent transcripts but no own root transcript
// (spec §10.1). The synthetic root carries orphanRoot=true in extras so the
// UI can hint at it. Idempotent: the ingester upserts on NativeID. resolvedRoot
// is the symlink-resolved projects root, threaded into the earliest-timestamp
// probe's containment open (P2.5b).
func emitOrphanRoots(ctx context.Context, resolvedRoot, sourceID string, transcripts []transcript, out chan<- canonical.Event) error {
	haveRoot := map[string]struct{}{}
	for _, t := range transcripts {
		if t.kind == canonical.KindRoot {
			haveRoot[t.nativeID] = struct{}{}
		}
	}
	// earliestTs per orphan parent across its children, for a meaningful Ts.
	orphans := map[string]int64{}
	order := []string{}
	for _, t := range transcripts {
		if t.kind != canonical.KindSubAgent {
			continue
		}
		if _, ok := haveRoot[t.parentNativeID]; ok {
			continue
		}
		ts := earliestTs(resolvedRoot, t.abs)
		if existing, seen := orphans[t.parentNativeID]; !seen || ts < existing {
			if !seen {
				order = append(order, t.parentNativeID)
			}
			orphans[t.parentNativeID] = ts
		}
	}
	sort.Strings(order)
	for _, parentID := range order {
		ev := canonical.SessionStartedEvent{
			EventBase: canonical.EventBase{
				SourceID:  sourceID,
				SourceSeq: 0,
				Ts:        orphans[parentID],
			},
			NativeID:     parentID,
			RootNativeID: parentID,
			Kind:         canonical.KindRoot,
			Extras:       map[string]any{"orphanRoot": true},
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case out <- ev:
		}
	}
	return nil
}

// earliestTs returns the first parseable record timestamp in a transcript,
// or 0 when none is found. Cheap single-line read for the orphan-root Ts. It
// opens the symlink-RESOLVED path within resolvedRoot (P2.5b, no TOCTOU): a path
// that resolves outside the root, or cannot be resolved, yields 0 (the synthetic
// orphan root then gets Ts=0 rather than reading an out-of-root file). Mirrors
// readTranscript's containment open.
func earliestTs(resolvedRoot, abs string) int64 {
	resolvedAbs, ok, rerr := withinResolvedRoot(resolvedRoot, abs)
	if rerr != nil || !ok {
		return 0
	}
	f, err := os.Open(resolvedAbs) // #nosec G304 -- opening the containment-checked RESOLVED path (withinResolvedRoot) from a filtered scan under the configured root
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), scanBufferMax)
	for sc.Scan() {
		rec, skip, perr := parseLine(sc.Bytes())
		if perr != nil || skip {
			continue
		}
		if rec.Env.Timestamp == "" {
			continue
		}
		if us, terr := parseTsToMicros(rec.Env.Timestamp); terr == nil {
			return us
		}
	}
	return 0
}

// emitProgress publishes a SourceProgressEvent with the current cursor.
func emitProgress(ctx context.Context, sourceID string, cur Cursor, out chan<- canonical.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ev := canonical.SourceProgressEvent{
		EventBase: canonical.EventBase{
			SourceID:  sourceID,
			SourceSeq: 0,
			Ts:        time.Now().UnixMicro(),
		},
		Cursor: cur.String(),
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case out <- ev:
		return nil
	}
}
