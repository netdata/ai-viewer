package aiagent_v2

import (
	"encoding/json"
	"hash/fnv"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// maxChildSessionDepth caps recursive opTree descent so a pathological
// snapshot cannot blow the stack or fill memory unbounded. Real data
// observed up to depth ~6; 32 leaves wide headroom while still bounding
// adversarial inputs. Exceed yields a SourceErrorEvent and the descent
// for that subtree stops.
const maxChildSessionDepth = 32

// stepIndexOffset shifts step indices into a high integer band so they
// never collide with turn indices on the canonical `turns.seq` column.
// See `adapter-aiagent-v2.md` §Canonical Model Gaps item 2.
const stepIndexOffset = 10000

// mapContext carries the per-file invariants every recursive emit
// needs. It is constructed once per snapshot and threaded through the
// walk; map-internal mutation is confined to err which surfaces a
// single fatal-per-file condition (e.g. depth overflow on the root).
type mapContext struct {
	sourceID string
	originID string
	filename string
	// sessionsRoot is the filesystem root for the v2 source (the same
	// directory `Adapter.root` points at). Used to resolve relative
	// `payload.ref.path` values against the canonical sessions tree
	// with the standard traversal guard. Empty string disables payload
	// ref resolution (tests that don't care can pass "").
	sessionsRoot string
	rootTs       int64
	rootTrace    string
}

type mapEmitter struct {
	ctx     *mapContext
	out     *[]canonical.Event
	onError func(error)
}

func (m *mapEmitter) append(ev canonical.Event) {
	*m.out = append(*m.out, ev)
}

func (m *mapEmitter) report(err error) {
	m.onError(err)
}

type sessionVisit struct {
	node           opTree
	parentNativeID string
	parentOpKey    string
	kind           canonical.SessionKind
	version        int
	depth          int
	jsonPointer    string
}

type opScope struct {
	sessionTrace   string
	turnSeq        int
	depth          int
	path           string
	jsonPointer    string
	stepKind       string
	stepAttributes map[string]json.RawMessage
}

type opVisit struct {
	op           operationNode
	scope        opScope
	seq          int
	reasoningSeq int
	path         string
	jsonPointer  string
}

type opTimes struct {
	startUs int64
	endUs   int64
}

// mapSnapshot converts a full opTree snapshot into the canonical event
// stream the ingester consumes. The function is pure: no I/O, no
// goroutines. Events emerge in opTree depth-first order so the
// canonical-events.md "chronological within a session" guarantee holds.
//
// onError surfaces non-fatal per-record conditions (depth cap exceeded,
// op time parse hiccups). Returns a fatal error only when the envelope
// itself is unusable.
func mapSnapshot(snap snapshot, sourceID, originID, sessionsRoot, filename string, onError func(error)) []canonical.Event {
	ctx := mapContext{
		sourceID:     sourceID,
		originID:     originID,
		filename:     filename,
		sessionsRoot: sessionsRoot,
		rootTs:       msToMicros(snap.OpTree.StartedAt),
		rootTrace:    snap.OpTree.TraceID,
	}
	out := make([]canonical.Event, 0, 64)
	emitter := mapEmitter{ctx: &ctx, out: &out, onError: onError}
	emitter.mapSession(sessionVisit{
		node:        snap.OpTree,
		kind:        canonical.KindRoot,
		version:     snap.Version,
		jsonPointer: "/opTree",
	})
	return out
}

// mapSessionStatus encodes the four-way decision tree from the spec.
// Both success and endedAt absent → in-progress (Running). Success
// true → Completed. Success false (with or without error) → Failed.
// EndedAt set with no success indicator → Interrupted.
func mapSessionStatus(node opTree) canonical.SessionStatus {
	if node.Success != nil {
		if *node.Success {
			return canonical.StatusCompleted
		}
		return canonical.StatusFailed
	}
	if node.EndedAt != nil {
		// Process exited mid-turn — interrupted per spec.
		return canonical.StatusInterrupted
	}
	if len(node.Turns) == 0 && len(node.Steps) == 0 {
		return canonical.StatusAbandoned
	}
	return canonical.StatusRunning
}

// mapOpKind translates source op kind strings to canonical OpKind.
// `system` lands on OpSystem; `session` on OpSession; `tool` on OpTool;
// `llm` on OpLLM. Unknown kinds fall through unchanged so a future
// producer addition is visible rather than silently re-mapped.
func mapOpKind(s string) canonical.OpKind {
	switch s {
	case "llm":
		return canonical.OpLLM
	case "tool":
		return canonical.OpTool
	case "session":
		return canonical.OpSession
	case "system":
		return canonical.OpSystem
	default:
		return canonical.OpKind(s)
	}
}

// mapOpStatus normalises op terminal status. v2 only writes `ok` or
// `failed`; absent status means running (the op hasn't finished yet in
// the snapshot we read).
func mapOpStatus(s string) string {
	switch s {
	case "ok":
		return "completed"
	case "failed":
		return "failed"
	case "":
		return "running"
	default:
		return s
	}
}

// turnStatusFromOps derives a turn-level status from its constituent
// ops. failed if any op failed; completed if every op completed;
// running otherwise.
func turnStatusFromOps(ops []operationNode) string {
	if len(ops) == 0 {
		return "completed"
	}
	allCompleted := true
	for i := range ops {
		switch ops[i].Status {
		case "failed":
			return "failed"
		case "ok":
			continue
		default:
			allCompleted = false
		}
	}
	if allCompleted {
		return "completed"
	}
	return "running"
}

// attrString pulls a JSON-encoded string value out of the attribute
// map. Returns "" when the key is absent or the value is not a string.
func attrString(attrs map[string]json.RawMessage, key string) string {
	raw, ok := attrs[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// normaliseSeverity maps producer severity codes onto the canonical
// four-level scale (DBG/INF/WRN/ERR). Unknown inputs default to INF so
// the UI still shows the log row.
func normaliseSeverity(s string) string {
	switch strings.ToUpper(s) {
	case "VRB", "DBG", "DEBUG":
		return "DBG"
	case "INF", "INFO":
		return "INF"
	case "WRN", "WARN", "WARNING":
		return "WRN"
	case "ERR", "ERROR", "FATAL":
		return "ERR"
	default:
		return "INF"
	}
}

// msToMicros converts a producer millisecond timestamp into canonical
// UNIX microseconds. Returns 0 when the input is non-positive so
// callers can detect "unset" and substitute the snapshot's root ts.
func msToMicros(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms * 1000
}

// endTsOrStarted returns endedAt (when set) or startedAt as the
// timestamp for a session-level event. Used for the SessionFinalized's
// Ts so a session with only startedAt still produces a coherent event.
func endTsOrStarted(node opTree) int64 {
	if node.EndedAt != nil {
		return msToMicros(*node.EndedAt)
	}
	return msToMicros(node.StartedAt)
}

// baseEvent constructs the canonical EventBase for an event whose
// per-file-path identity is `path`. SourceSeq is FNV-64 of
// `originId + NUL + path` — a deterministic, stable-across-rescans event
// identifier (observability counter; NOT a dedup gate). Re-emission on
// rescan is absorbed by the ingester's SQL-layer idempotent upserts,
// which key on each table's natural identity, not on SourceSeq. See
// ingester.md §Dedup and Idempotency.
func baseEvent(ctx *mapContext, path string, ts int64) canonical.EventBase {
	return canonical.EventBase{
		SourceID:  ctx.sourceID,
		SourceSeq: seqForPath(ctx.originID, path),
		Ts:        ts,
	}
}

// seqForPath returns a stable 63-bit SourceSeq derived from the
// (originId, path) tuple. FNV-64 chosen over xxhash for stdlib-only
// dependency footprint; collision probability across one source's
// events is negligible at this scale (294K files × ~thousand events
// each ≪ 2^63).
func seqForPath(originID, path string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(originID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(path))
	// Mask off the sign bit so any downstream conversion to int64 stays
	// positive without losing meaningful entropy.
	return h.Sum64() & 0x7FFFFFFFFFFFFFFF
}
