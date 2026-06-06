package claude_code

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

const (
	claudeBenchScanExpectedEvents int64 = 2058
	claudeBenchTailExpectedEvents int64 = 6
)

// BenchmarkClaudeScan_SyntheticCorpus exercises first backfill over a
// deterministic projects tree with root transcripts, subagent transcripts, and
// sidecar metas. The corpus is materialized under b.TempDir() so the benchmark
// never reads operator Claude data.
func BenchmarkClaudeScan_SyntheticCorpus(b *testing.B) {
	root := b.TempDir()
	totalBytes, transcriptCount := buildClaudeBenchCorpus(b, root)

	var scanErrors claudeBenchErrorRecorder
	a, err := New(root, canonical.AdapterOptions{OnError: scanErrors.onError})
	if err != nil {
		b.Fatalf("new adapter: %v", err)
	}

	b.ReportAllocs()
	b.SetBytes(totalBytes)
	b.ResetTimer()

	var lastEvents int64
	for i := 0; i < b.N; i++ {
		scanErrors.reset()
		lastEvents = runClaudeBenchScan(b, a)
		scanErrors.assertEmpty(b, "scan")
		assertClaudeBenchEventCount(b, "scan", lastEvents, claudeBenchScanExpectedEvents)
	}
	b.StopTimer()

	wallSec := b.Elapsed().Seconds() / float64(max(b.N, 1))
	if wallSec <= 0 {
		wallSec = 1e-9
	}
	b.ReportMetric(float64(transcriptCount)/wallSec, "transcripts/sec")
	b.ReportMetric(float64(lastEvents)/wallSec, "events/sec")
}

// BenchmarkClaudeTail_SyntheticAppend measures the deterministic flush path used
// by Tail after fsnotify/debounce has identified dirty files. The producer's
// append/write is fenced out with StopTimer/StartTimer, so scheduler and disk
// write jitter do not pollute the regression signal.
func BenchmarkClaudeTail_SyntheticAppend(b *testing.B) {
	root := b.TempDir()
	rel, path, seedBody, appendBodies, seedCursor := buildClaudeTailBenchFixture(b, root)
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		b.Fatalf("resolve root: %v", err)
	}

	out := make(chan canonical.Event, 256)
	dirty := map[string]struct{}{rel: {}}
	metaDirty := map[string]struct{}{}
	def := newTailDeferral()
	sourceID := sourceIDPrefix + root

	b.ReportAllocs()
	// Tail flush replays the whole file from offset 0 to rebuild mapper state.
	b.SetBytes(int64(len(seedBody) + len(appendBodies[0])))
	b.ResetTimer()

	var flushErrors claudeBenchErrorRecorder
	var lastEvents int64
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		body := make([]byte, 0, len(seedBody)+len(appendBodies[i%len(appendBodies)]))
		body = append(body, seedBody...)
		body = append(body, appendBodies[i%len(appendBodies)]...)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			b.Fatalf("write append variant %d: %v", i, err)
		}
		cur := seedCursor
		b.StartTimer()

		flushErrors.reset()
		flush := newTailFlush(context.Background(), resolvedRoot, root, sourceID, &cur, def, out, flushErrors.onError)
		if err := flush.flushDirty(dirty, metaDirty); err != nil {
			b.Fatalf("flush append variant %d: %v", i, err)
		}
		flushErrors.assertEmpty(b, "flush append")
		lastEvents = drainClaudeBenchEvents(out)
		assertClaudeBenchEventCount(b, "tail append", lastEvents, claudeBenchTailExpectedEvents)
	}
	b.StopTimer()

	wallSec := b.Elapsed().Seconds() / float64(max(b.N, 1))
	if wallSec <= 0 {
		wallSec = 1e-9
	}
	b.ReportMetric(float64(lastEvents)/wallSec, "events/sec")
}

func buildClaudeBenchCorpus(b *testing.B, root string) (totalBytes int64, transcriptCount int) {
	b.Helper()
	const (
		projects           = 4
		sessionsPerProject = 16
	)
	for project := 0; project < projects; project++ {
		projectName := fmt.Sprintf("project-%02d", project)
		projectDir := filepath.Join(root, projectName)
		for session := 0; session < sessionsPerProject; session++ {
			ordinal := project*sessionsPerProject + session
			sessionID := fmt.Sprintf("sess-%02d-%03d", project, session)
			agentID := fmt.Sprintf("agent%03dabcdef", ordinal)
			hasSubagent := session%4 == 0

			rootBody := buildClaudeRootTranscript(b, sessionID, agentID, ordinal, hasSubagent)
			writeClaudeBenchFile(b, filepath.Join(projectDir, sessionID+".jsonl"), rootBody)
			totalBytes += int64(len(rootBody))
			transcriptCount++

			if !hasSubagent {
				continue
			}
			subDir := filepath.Join(projectDir, sessionID, subagentsDir)
			subBody := buildClaudeSubagentTranscript(b, sessionID, agentID, ordinal)
			writeClaudeBenchFile(b, filepath.Join(subDir, "agent-"+agentID+".jsonl"), subBody)
			metaBody := buildClaudeSubagentMeta(b, agentID)
			writeClaudeBenchFile(b, filepath.Join(subDir, "agent-"+agentID+".meta.json"), metaBody)
			totalBytes += int64(len(subBody) + len(metaBody))
			transcriptCount++
		}
	}
	return totalBytes, transcriptCount
}

func buildClaudeTailBenchFixture(b *testing.B, root string) (rel, path string, seedBody []byte, appendBodies [2][]byte, seedCursor Cursor) {
	b.Helper()
	projectDir := filepath.Join(root, "tail-project")
	sessionID := "tail-session"
	rel = filepath.ToSlash(filepath.Join("tail-project", sessionID+".jsonl"))
	path = filepath.Join(projectDir, sessionID+".jsonl")
	seedBody = []byte(claudeBenchUserPromptLine(b, sessionID, "", "tail-seed-user", 0, "seed", 320))
	writeClaudeBenchFile(b, path, seedBody)

	cur := newCursor()
	out := make(chan canonical.Event, 128)
	dirty := map[string]struct{}{rel: {}}
	resolvedRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		b.Fatalf("resolve root: %v", err)
	}
	var flushErrors claudeBenchErrorRecorder
	flush := newTailFlush(context.Background(), resolvedRoot, root, sourceIDPrefix+root, &cur, newTailDeferral(), out, flushErrors.onError)
	if err := flush.flushDirty(dirty, map[string]struct{}{}); err != nil {
		b.Fatalf("prime tail cursor: %v", err)
	}
	flushErrors.assertEmpty(b, "prime tail cursor")
	_ = drainClaudeBenchEvents(out)

	appendBodies[0] = []byte(claudeBenchAssistantLine(b, sessionID, "tail-append-a", 1, []benchContentBlock{
		{Type: "thinking", Thinking: claudeBenchPad(101, 160), Signature: "sig-a"},
		{Type: "text", Text: "synthetic tail append variant A " + claudeBenchPad(102, 160)},
	}, "end_turn"))
	appendBodies[1] = []byte(claudeBenchAssistantLine(b, sessionID, "tail-append-b", 1, []benchContentBlock{
		{Type: "thinking", Thinking: claudeBenchPad(201, 160), Signature: "sig-b"},
		{Type: "text", Text: "synthetic tail append variant B " + claudeBenchPad(202, 160)},
	}, "end_turn"))
	return rel, path, seedBody, appendBodies, cur
}

func buildClaudeRootTranscript(b *testing.B, sessionID, agentID string, ordinal int, hasSubagent bool) []byte {
	b.Helper()
	var sb strings.Builder
	for turn := 0; turn < 4; turn++ {
		sb.WriteString(claudeBenchUserPromptLine(b, sessionID, "", fmt.Sprintf("u-%03d-%02d", ordinal, turn), turn*3, "root", 220))
		blocks := []benchContentBlock{
			{Type: "thinking", Thinking: claudeBenchPad(ordinal*100+turn, 120), Signature: fmt.Sprintf("sig-%03d-%02d", ordinal, turn)},
			{Type: "text", Text: "synthetic assistant text " + claudeBenchPad(ordinal*200+turn, 180)},
			{
				Type:  "tool_use",
				ID:    fmt.Sprintf("toolu_read_%03d_%02d", ordinal, turn),
				Name:  "Read",
				Input: claudeBenchRaw(b, benchReadInput{FilePath: fmt.Sprintf("synthetic-%03d-%02d.txt", ordinal, turn)}),
			},
		}
		if hasSubagent && turn == 0 {
			blocks = append(blocks, benchContentBlock{
				Type: "tool_use",
				ID:   claudeBenchAgentToolUseID(agentID),
				Name: "Agent",
				Input: claudeBenchRaw(b, benchAgentInput{
					Description:  "synthetic subagent task",
					SubagentType: "general-purpose",
				}),
			})
		}
		sb.WriteString(claudeBenchAssistantLine(b, sessionID, fmt.Sprintf("a-%03d-%02d", ordinal, turn), turn*3+1, blocks, "tool_use"))
		sb.WriteString(claudeBenchToolResultLine(b, sessionID, fmt.Sprintf("tr-%03d-%02d", ordinal, turn), turn*3+2, fmt.Sprintf("toolu_read_%03d_%02d", ordinal, turn), 180))
	}
	return []byte(sb.String())
}

func buildClaudeSubagentTranscript(b *testing.B, sessionID, agentID string, ordinal int) []byte {
	b.Helper()
	var sb strings.Builder
	sb.WriteString(claudeBenchUserPromptLine(b, sessionID, agentID, fmt.Sprintf("su-%03d", ordinal), 20, "subagent", 180))
	sb.WriteString(claudeBenchAssistantLine(b, sessionID, fmt.Sprintf("sa-%03d", ordinal), 21, []benchContentBlock{
		{Type: "text", Text: "synthetic subagent completion " + claudeBenchPad(ordinal*300, 220)},
	}, "end_turn"))
	return []byte(sb.String())
}

func buildClaudeSubagentMeta(b *testing.B, agentID string) []byte {
	b.Helper()
	raw := claudeBenchRaw(b, benchSubagentMeta{
		AgentType:   "general-purpose",
		Description: "synthetic subagent task",
		ToolUseID:   claudeBenchAgentToolUseID(agentID),
	})
	return raw
}

func runClaudeBenchScan(b *testing.B, a *Adapter) int64 {
	b.Helper()
	out := make(chan canonical.Event, 1024)
	done := make(chan int64, 1)
	go func() {
		done <- drainClaudeBenchEventsUntilClosed(out)
	}()
	if err := a.Scan(context.Background(), nil, out); err != nil {
		close(out)
		<-done
		b.Fatalf("scan: %v", err)
	}
	close(out)
	return <-done
}

type claudeBenchErrorRecorder struct {
	errs []error
}

func (r *claudeBenchErrorRecorder) onError(err error) {
	if err != nil {
		r.errs = append(r.errs, err)
	}
}

func (r *claudeBenchErrorRecorder) reset() {
	r.errs = r.errs[:0]
}

func (r *claudeBenchErrorRecorder) assertEmpty(b *testing.B, stage string) {
	b.Helper()
	if len(r.errs) == 0 {
		return
	}
	b.Fatalf("%s reported %d adapter errors; first: %v", stage, len(r.errs), r.errs[0])
}

func assertClaudeBenchEventCount(b *testing.B, stage string, got, want int64) {
	b.Helper()
	if got != want {
		b.Fatalf("%s emitted %d events, want %d", stage, got, want)
	}
}

func drainClaudeBenchEventsUntilClosed(ch <-chan canonical.Event) int64 {
	var count int64
	for range ch {
		count++
	}
	return count
}

func drainClaudeBenchEvents(ch <-chan canonical.Event) int64 {
	var count int64
	for {
		select {
		case <-ch:
			count++
		default:
			return count
		}
	}
}

func writeClaudeBenchFile(b *testing.B, path string, body []byte) {
	b.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		b.Fatalf("write %s: %v", path, err)
	}
}

func claudeBenchUserPromptLine(b *testing.B, sessionID, agentID, uuid string, tsIndex int, label string, pad int) string {
	b.Helper()
	msg := benchUserMessage{
		Role:    "user",
		Content: claudeBenchRaw(b, "synthetic "+label+" prompt "+claudeBenchPad(tsIndex, pad)),
	}
	rec := benchEnvelope{
		Type:        "user",
		UUID:        uuid,
		SessionID:   sessionID,
		Timestamp:   claudeBenchTimestamp(tsIndex),
		Message:     claudeBenchRaw(b, msg),
		IsSidechain: agentID != "",
		AgentID:     agentID,
	}
	return claudeBenchLine(b, rec)
}

func claudeBenchAssistantLine(b *testing.B, sessionID, uuid string, tsIndex int, blocks []benchContentBlock, stopReason string) string {
	b.Helper()
	msg := benchAssistantMessage{
		ID:         "msg_" + uuid,
		Role:       "assistant",
		Model:      "claude-opus-4-7",
		StopReason: stopReason,
		Usage: benchAssistantUsage{
			InputTokens:              120 + int64(tsIndex),
			OutputTokens:             40 + int64(len(blocks)),
			CacheCreationInputTokens: 3,
			CacheReadInputTokens:     17,
			ServerToolUse: claudeBenchRaw(b, benchServerToolUse{
				WebSearchRequests: int64(tsIndex % 3),
			}),
			ServiceTier: "standard",
		},
		Content: blocks,
	}
	return claudeBenchLine(b, benchEnvelope{
		Type:      "assistant",
		UUID:      uuid,
		SessionID: sessionID,
		Timestamp: claudeBenchTimestamp(tsIndex),
		Message:   claudeBenchRaw(b, msg),
	})
}

func claudeBenchToolResultLine(b *testing.B, sessionID, uuid string, tsIndex int, toolID string, pad int) string {
	b.Helper()
	blocks := []benchContentBlock{{
		Type:      "tool_result",
		ToolUseID: toolID,
		Content:   "synthetic tool result " + claudeBenchPad(tsIndex, pad),
	}}
	msg := benchUserMessage{
		Role:    "user",
		Content: claudeBenchRaw(b, blocks),
	}
	return claudeBenchLine(b, benchEnvelope{
		Type:      "user",
		UUID:      uuid,
		SessionID: sessionID,
		Timestamp: claudeBenchTimestamp(tsIndex),
		Message:   claudeBenchRaw(b, msg),
	})
}

func claudeBenchLine(b *testing.B, rec benchEnvelope) string {
	b.Helper()
	raw := claudeBenchRaw(b, rec)
	return string(raw) + "\n"
}

func claudeBenchRaw[T any](b *testing.B, v T) json.RawMessage {
	b.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		b.Fatalf("marshal bench record: %v", err)
	}
	return raw
}

func claudeBenchTimestamp(index int) string {
	return fmt.Sprintf("2026-05-26T10:%02d:%02d.000Z", (index/60)%60, index%60)
}

func claudeBenchAgentToolUseID(agentID string) string {
	return "toolu_" + agentID
}

func claudeBenchPad(seed, length int) string {
	var sb strings.Builder
	sb.Grow(length)
	state := uint32(seed*2654435761 + 1)
	for i := 0; i < length; i++ {
		state = state*1664525 + 1013904223
		sb.WriteByte(byte('a' + state%26))
	}
	return sb.String()
}
