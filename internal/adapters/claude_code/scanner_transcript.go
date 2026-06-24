package claude_code

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// readTranscript parses one transcript from offset 0 to EOF, emits only lines
// at or after the cursor offset, and returns the updated FileCursor plus the
// mapper state rebuilt by the replay.
func readTranscript(ctx context.Context, root string, t transcript, sourceID string, mm metaMap, start FileCursor, out chan<- canonical.Event, onError func(error)) (FileCursor, int, *fileMapper, error) {
	reader := transcriptReader{
		ctx:      ctx,
		root:     root,
		t:        t,
		sourceID: sourceID,
		metas:    mm,
		start:    start,
		out:      out,
		onError:  ensureOnError(onError),
	}
	return reader.read()
}

type transcriptReader struct {
	ctx      context.Context
	root     string
	t        transcript
	sourceID string
	metas    metaMap
	start    FileCursor
	out      chan<- canonical.Event
	onError  func(error)
}

func (r transcriptReader) read() (FileCursor, int, *fileMapper, error) {
	f, size, err := r.open()
	if err != nil {
		return r.start, 0, nil, err
	}
	defer func() { _ = f.Close() }()

	cur := r.cursorForSize(size)
	emitFrom := clampEmitFrom(cur.Offset, size)
	mapper := r.newMapper()
	emitted, advanced, err := streamLines(r.ctx, f, emitFrom, r.t, mapper, r.out, r.onError)
	if err != nil {
		return cur, emitted, mapper, err
	}
	cur.Offset = advanced
	cur.Size = size
	mapper.fullyRead = advanced >= size
	return cur, emitted, mapper, nil
}

func (r transcriptReader) open() (*os.File, int64, error) {
	resolvedAbs, ok, err := resolveWithinRoot(r.root, r.t.abs)
	if err != nil {
		return nil, 0, fmt.Errorf("claude_code: cannot resolve %s for containment; skipping: %w", r.t.abs, err)
	}
	if !ok {
		return nil, 0, fmt.Errorf("claude_code: %s resolves outside the projects root; skipping (symlink escape)", r.t.rel)
	}
	f, err := os.Open(resolvedAbs) // #nosec G304 -- opening the containment-checked RESOLVED path (resolveWithinRoot) from a filtered scan under the configured root
	if err != nil {
		return nil, 0, fmt.Errorf("open %s: %w", r.t.abs, err)
	}
	size, statErr := statTranscript(f, r.t.abs)
	if statErr != nil {
		_ = f.Close()
		return nil, 0, statErr
	}
	return f, size, nil
}

func statTranscript(f *os.File, abs string) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", abs, err)
	}
	return info.Size(), nil
}

func (r transcriptReader) cursorForSize(size int64) FileCursor {
	cur := r.start
	if cur.Size > 0 && size < cur.Size {
		r.onError(fmt.Errorf("transcript %s shrank (size=%d, cursor.size=%d); rescanning from 0", r.t.rel, size, cur.Size))
		return FileCursor{}
	}
	return cur
}

func clampEmitFrom(offset, size int64) int64 {
	if offset > size {
		return size
	}
	return offset
}

func (r transcriptReader) newMapper() *fileMapper {
	return newFileMapper(mapperConfig{
		sourceID:       r.sourceID,
		absPath:        r.t.abs,
		nativeID:       r.t.nativeID,
		parentNativeID: r.t.parentNativeID,
		kind:           r.t.kind,
		agentName:      agentNameFor(r.t, r.metas),
		toolUseID:      toolUseIDFor(r.t, r.metas),
		toolUseToAgent: r.metas.toolUseToAgent,
		root:           r.root,
		sessionDir:     r.t.sessionDir,
	})
}

// agentNameFor returns the AgentName a transcript's session should start with.
func agentNameFor(t transcript, mm metaMap) string {
	if t.kind != canonical.KindSubAgent {
		return ""
	}
	agentID := agentIDFromNative(t.nativeID)
	return mm.agentType[agentID]
}

// toolUseIDFor returns the child's own toolUseId for a sub_agent transcript.
func toolUseIDFor(t transcript, mm metaMap) string {
	if t.kind != canonical.KindSubAgent || mm.agentToolUse == nil {
		return ""
	}
	return mm.agentToolUse[agentIDFromNative(t.nativeID)]
}

// agentIDFromNative extracts the agentId from a synthetic subagent NativeID
// ("<parent>:agent:<agentId>").
func agentIDFromNative(nativeID string) string {
	if i := strings.LastIndex(nativeID, ":agent:"); i >= 0 {
		return nativeID[i+len(":agent:"):]
	}
	return ""
}

// streamLines reads newline-terminated JSON records from r positioned at
// offset 0. It rebuilds mapper state for every complete physical line and
// emits only records whose line starts at or after emitFrom.
func streamLines(ctx context.Context, r io.Reader, emitFrom int64, t transcript, mapper *fileMapper, out chan<- canonical.Event, onError func(error)) (int, int64, error) {
	streamer := lineStreamer{
		ctx:      ctx,
		br:       bufio.NewReaderSize(r, 64*1024),
		emitFrom: emitFrom,
		t:        t,
		mapper:   mapper,
		out:      out,
		onError:  onError,
	}
	emitted, off, err := streamer.run()
	// Flush a deferred SessionStarted at EOF (SOW-0028) so a file that opened
	// with only timestamp-less records still creates its session row. ctx
	// cancellation suppresses the tail emit (we are shutting down); a real
	// emit error is reported alongside the loop error.
	if ferr := streamer.finalize(); ferr != nil && err == nil {
		err = ferr
	}
	return emitted, off, err
}

type lineStreamer struct {
	ctx      context.Context
	br       *bufio.Reader
	emitFrom int64
	t        transcript
	mapper   *fileMapper
	out      chan<- canonical.Event
	onError  func(error)
	emitted  int
	off      int64
	lineNo   int64
}

func (s *lineStreamer) run() (int, int64, error) {
	for {
		done, err := s.step()
		if done || err != nil {
			return s.emitted, s.off, err
		}
	}
}

// finalize flushes a deferred SessionStarted at EOF (SOW-0028). No-op once the
// session started during mapRecord. Emits the tail events (SessionStarted +
// any buffered leading-snapshot events) for a file that never saw a
// timestamped record. Called by streamLines after the per-record loop ends.
func (s *lineStreamer) finalize() error {
	events := s.mapper.finalizeSessionStart()
	if len(events) == 0 {
		return nil
	}
	return s.emitEvents(events)
}

func (s *lineStreamer) step() (bool, error) {
	if err := s.ctx.Err(); err != nil {
		return true, err
	}
	line, consumed, err := readOneLine(s.br)
	if err != nil {
		return s.handleReadError(consumed, err)
	}
	if len(line) == 0 {
		return true, nil
	}
	return false, s.processLine(line)
}

func (s *lineStreamer) handleReadError(consumed int64, err error) (bool, error) {
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	if !errors.Is(err, errLineTooLong) {
		return true, fmt.Errorf("read %s @%d: %w", s.t.rel, s.off, err)
	}
	s.handleOversizedLine(consumed)
	return false, nil
}

func (s *lineStreamer) handleOversizedLine(consumed int64) {
	s.lineNo++
	emit := s.off >= s.emitFrom
	if emit {
		s.onError(fmt.Errorf("transcript %s @%d: line exceeds %d bytes; skipping", s.t.rel, s.off, scanBufferMax))
	}
	s.mapper.lastRecordAssistantText = false
	s.mapper.lastRecordEmitted = emit
	s.off += consumed
}

func (s *lineStreamer) processLine(line []byte) error {
	s.lineNo++
	recBytes, lineStart := s.consumeLine(line)
	emit := lineStart >= s.emitFrom
	rec, skip, err := parseLine(recBytes)
	if err != nil {
		s.handleParseError(lineStart, err, emit)
		return nil
	}
	if skip {
		s.markUnmappedPhysicalLine(emit)
		return nil
	}
	return s.mapAndMaybeEmit(lineStart, rec, emit)
}

func (s *lineStreamer) consumeLine(line []byte) ([]byte, int64) {
	lineStart := s.off
	s.off += int64(len(line))
	return line[:len(line)-1], lineStart
}

func (s *lineStreamer) handleParseError(lineStart int64, err error, emit bool) {
	if emit && shouldSurfaceParseError(s.mapper, err) {
		s.onError(fmt.Errorf("transcript %s @%d: %w", s.t.rel, lineStart, err))
	}
	s.markUnmappedPhysicalLine(emit)
}

func (s *lineStreamer) markUnmappedPhysicalLine(emit bool) {
	s.mapper.lastRecordAssistantText = false
	s.mapper.lastRecordEmitted = emit
}

func (s *lineStreamer) mapAndMaybeEmit(lineStart int64, rec record, emit bool) error {
	rec.LineNo = s.lineNo
	events, err := s.mapper.mapRecord(rec)
	if err != nil {
		s.handleMapError(lineStart, err, emit)
		return nil
	}
	s.mapper.lastRecordEmitted = emit
	if !emit {
		return nil
	}
	return s.emitEvents(events)
}

func (s *lineStreamer) handleMapError(lineStart int64, err error, emit bool) {
	if emit {
		s.onError(fmt.Errorf("transcript %s @%d: map: %w", s.t.rel, lineStart, err))
	}
	s.mapper.lastRecordEmitted = emit
}

func (s *lineStreamer) emitEvents(events []canonical.Event) error {
	for _, ev := range events {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case s.out <- ev:
			s.emitted++
		}
	}
	return nil
}

// shouldSurfaceParseError reports whether a per-line parse error should be
// forwarded to onError. Unknown-`type` errors are deduped to one per distinct
// variant per file; all other parse errors surface every time.
func shouldSurfaceParseError(mapper *fileMapper, perr error) bool {
	var ute *unknownTypeError
	if errors.As(perr, &ute) {
		return mapper.firstUnknownType(ute.Type)
	}
	return true
}

// errLineTooLong signals that a single transcript line exceeded scanBufferMax.
var errLineTooLong = errors.New("claude_code: line exceeds scan buffer")

// readOneLine reads one '\n'-terminated record from br. Partial trailing lines
// return io.EOF with consumed=0 so callers hold them back.
func readOneLine(br *bufio.Reader) ([]byte, int64, error) {
	buf := make([]byte, 0, 256)
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return incompleteLineOrError(err)
		}
		if len(buf) > scanBufferMax {
			return oversizedLineRead(br, len(buf), err)
		}
		if err == nil {
			return buf, int64(len(buf)), nil
		}
	}
}

func oversizedLineRead(br *bufio.Reader, consumed int, err error) ([]byte, int64, error) {
	if !errors.Is(err, bufio.ErrBufferFull) {
		return nil, int64(consumed), errLineTooLong
	}
	drained, drainErr := drainToNewline(br)
	if errors.Is(drainErr, io.EOF) {
		return nil, 0, io.EOF
	}
	if drainErr != nil {
		return nil, 0, drainErr
	}
	return nil, int64(consumed) + drained, errLineTooLong
}

func incompleteLineOrError(err error) ([]byte, int64, error) {
	if errors.Is(err, io.EOF) {
		return nil, 0, io.EOF
	}
	return nil, 0, err
}

// drainToNewline reads and discards bytes from br up to and including the next
// '\n', returning the number of bytes consumed.
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
