package parity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

const opencodeFormat = "opencode"

// OpencodeSourceOptions configures source-manifest extraction for opencode.
type OpencodeSourceOptions struct {
	DBPath   string
	SourceID string
}

// ExtractOpencodeSource builds a source manifest by reading opencode SQLite
// tables directly. It does not call the opencode adapter mapper.
func ExtractOpencodeSource(ctx context.Context, opts OpencodeSourceOptions) ([]Artifact, error) {
	var artifacts []Artifact
	err := ExtractOpencodeSourceToWriter(ctx, opts, ArtifactWriterFunc(func(ctx context.Context, artifact Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
		return nil
	}))
	return artifacts, err
}

// ExtractOpencodeSourceToWriter streams source artifacts by reading opencode
// SQLite tables directly. It does not call the opencode adapter mapper.
func ExtractOpencodeSourceToWriter(ctx context.Context, opts OpencodeSourceOptions, writer ArtifactWriter) error {
	if opts.DBPath == "" {
		return fmt.Errorf("opencode source database path is required")
	}
	if writer == nil {
		return fmt.Errorf("extract opencode source: nil artifact writer")
	}
	sourceID := opts.SourceID
	if sourceID == "" {
		sourceID = opencodeFormat + ":" + opts.DBPath
	}
	db, err := openOpencodeParityReadOnly(opts.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	tx, err := beginOpencodeSourceReadSnapshot(ctx, db)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	state := &opencodeSourceState{
		sourceID: sourceID,
		dbPath:   opts.DBPath,
		querier:  tx,
		ctx:      ctx,
		writer:   writer,
	}
	if err := state.extract(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("close opencode source read snapshot: %w", err)
	}
	committed = true
	return nil
}

type opencodeSourceQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type opencodeSourceState struct {
	sourceID string
	dbPath   string
	querier  opencodeSourceQuerier
	ctx      context.Context
	writer   ArtifactWriter

	hasSessionInput          bool
	hasSessionMessage        bool
	sessionMessageHasSeq     bool
	messagesBySession        map[string][]opencodeSourceMessage
	partsByMessage           map[string][]opencodeSourcePart
	inputsByID               map[string]opencodeSourceInput
	sessionMessagesBySession map[string][]opencodeSourceSessionMessage
}

func (s *opencodeSourceState) emit(artifact Artifact) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	return s.writer.WriteArtifact(s.ctx, artifact)
}

type opencodeSourceSession struct {
	ID             string
	ParentID       string
	ProjectID      string
	Slug           string
	Directory      string
	Title          string
	Version        string
	Agent          string
	Model          sql.NullString
	TimeCreatedMs  int64
	TimeArchivedMs sql.NullInt64
}

type opencodeSourceMessage struct {
	ID            string
	SessionID     string
	TimeCreatedMs int64
	Data          []byte
}

type opencodeSourcePart struct {
	ID            string
	MessageID     string
	SessionID     string
	TimeCreatedMs int64
	Data          []byte
}

type opencodeSourceInput struct {
	ID            string
	SessionID     string
	Prompt        []byte
	TimeCreatedMs int64
}

type opencodeSourceSessionMessage struct {
	ID            string
	SessionID     string
	Type          string
	Seq           int64
	TimeCreatedMs int64
	Data          []byte
}

type opencodeSourceMessageData struct {
	Role     string                        `json:"role"`
	ParentID string                        `json:"parentID"`
	ModelID  string                        `json:"modelID"`
	Time     opencodeSourceMessageTime     `json:"time"`
	Error    *opencodeSourceAssistantError `json:"error"`
	Provider string                        `json:"providerID"`
}

type opencodeSourceMessageTime struct {
	Completed *int64 `json:"completed"`
}

type opencodeSourceAssistantError struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

type opencodeSourcePartData struct {
	Type     string                   `json:"type"`
	Text     string                   `json:"text"`
	Time     opencodeSourcePartTime   `json:"time"`
	Tool     string                   `json:"tool"`
	State    *opencodeSourceToolState `json:"state"`
	Auto     bool                     `json:"auto"`
	Attempt  int                      `json:"attempt"`
	Hash     string                   `json:"hash"`
	Files    []string                 `json:"files"`
	Error    opencodeSourcePartError  `json:"error"`
	MIME     string                   `json:"mime"`
	Filename string                   `json:"filename"`
	URL      string                   `json:"url"`
}

type opencodeSourcePartTime struct {
	Start int64  `json:"start"`
	End   *int64 `json:"end"`
}

type opencodeSourceToolState struct {
	Status   string                 `json:"status"`
	Input    json.RawMessage        `json:"input"`
	Output   string                 `json:"output"`
	Error    string                 `json:"error"`
	Time     opencodeSourcePartTime `json:"time"`
	Metadata json.RawMessage        `json:"metadata"`
}

type opencodeSourcePartError struct {
	Name string `json:"name"`
}

type opencodeSourceTurnScope struct {
	sessionID string
	turnSeq   int64
	modelID   string
}

type opencodeSourceOp struct {
	kind          string
	name          string
	toolNamespace string
	status        string
	startedAt     int64
	endedAt       *int64
	errorClass    string
	errorMessage  string
	childID       string
}

type opencodeSourceTurnState struct {
	scope    opencodeSourceTurnScope
	opSeq    int64
	llmOpSeq int64
	llmOpen  bool
	ops      map[int64]opencodeSourceOp
}

func (s *opencodeSourceState) prepareSchema() error {
	hasSessionInput, err := opencodeSourceTableExists(s.ctx, s.querier, "session_input")
	if err != nil {
		return err
	}
	s.hasSessionInput = hasSessionInput

	hasSessionMessage, err := opencodeSourceTableExists(s.ctx, s.querier, "session_message")
	if err != nil {
		return err
	}
	s.hasSessionMessage = hasSessionMessage
	if !hasSessionMessage {
		return nil
	}
	columns, err := opencodeSourceTableColumns(s.ctx, s.querier, "session_message")
	if err != nil {
		return err
	}
	_, s.sessionMessageHasSeq = columns["seq"]
	return nil
}

func (s *opencodeSourceState) loadSessionRows(sessionID string) error {
	messages, err := loadOpencodeSourceMessages(s.ctx, s.querier, sessionID)
	if err != nil {
		return err
	}
	parts, err := loadOpencodeSourceParts(s.ctx, s.querier, sessionID)
	if err != nil {
		return err
	}
	inputs, err := loadOpencodeSourceInputs(s.ctx, s.querier, sessionID, s.hasSessionInput)
	if err != nil {
		return err
	}
	sessionMessages, err := loadOpencodeSourceSessionMessages(
		s.ctx,
		s.querier,
		sessionID,
		s.hasSessionMessage,
		s.sessionMessageHasSeq,
	)
	if err != nil {
		return err
	}

	s.messagesBySession = map[string][]opencodeSourceMessage{sessionID: messages}
	s.partsByMessage = map[string][]opencodeSourcePart{}
	s.inputsByID = make(map[string]opencodeSourceInput, len(inputs))
	s.sessionMessagesBySession = map[string][]opencodeSourceSessionMessage{sessionID: sessionMessages}
	for _, part := range parts {
		s.partsByMessage[part.MessageID] = append(s.partsByMessage[part.MessageID], part)
	}
	for _, input := range inputs {
		s.inputsByID[input.ID] = input
	}
	return nil
}

func (s *opencodeSourceState) clearSessionRows() {
	s.messagesBySession = nil
	s.partsByMessage = nil
	s.inputsByID = nil
	s.sessionMessagesBySession = nil
}

func (s *opencodeSourceState) streamSessions(fn func(opencodeSourceSession) error) error {
	sessions, err := s.loadSessions()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if err := fn(session); err != nil {
			return err
		}
	}
	return nil
}

func (s *opencodeSourceState) loadSessions() ([]opencodeSourceSession, error) {
	columns, err := opencodeSourceTableColumns(s.ctx, s.querier, "session")
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(
		`SELECT id, COALESCE(parent_id, ''), project_id, slug, directory, title, version, %s, %s, time_created, %s FROM session ORDER BY id`,
		opencodeSourceTextColumn(columns, "agent"),
		opencodeSourceNullableColumn(columns, "model"),
		opencodeSourceNullableColumn(columns, "time_archived"),
	)
	rows, err := s.querier.QueryContext(s.ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query opencode source sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sessions []opencodeSourceSession
	for rows.Next() {
		var session opencodeSourceSession
		if err := rows.Scan(
			&session.ID,
			&session.ParentID,
			&session.ProjectID,
			&session.Slug,
			&session.Directory,
			&session.Title,
			&session.Version,
			&session.Agent,
			&session.Model,
			&session.TimeCreatedMs,
			&session.TimeArchivedMs,
		); err != nil {
			return nil, fmt.Errorf("scan opencode source session: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate opencode source sessions: %w", err)
	}
	return sessions, nil
}

func opencodeSourceTableColumns(ctx context.Context, querier opencodeSourceQuerier, table string) (map[string]struct{}, error) {
	if table != "session" && table != "session_message" {
		return nil, fmt.Errorf("opencode source column introspection does not support table %q", table)
	}
	rows, err := querier.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("query opencode source table columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	columns := map[string]struct{}{}
	for rows.Next() {
		var (
			cid       int
			name      string
			columnTyp sql.NullString
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &columnTyp, &notNull, &dfltValue, &pk); err != nil {
			return nil, fmt.Errorf("scan opencode source table column: %w", err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate opencode source table columns: %w", err)
	}
	return columns, nil
}

func opencodeSourceTextColumn(columns map[string]struct{}, column string) string {
	if _, ok := columns[column]; ok {
		return "COALESCE(" + column + ", '') AS " + column
	}
	return "'' AS " + column
}

func opencodeSourceNullableColumn(columns map[string]struct{}, column string) string {
	if _, ok := columns[column]; ok {
		return column
	}
	return "NULL AS " + column
}

func loadOpencodeSourceMessages(ctx context.Context, querier opencodeSourceQuerier, sessionID string) ([]opencodeSourceMessage, error) {
	rows, err := querier.QueryContext(ctx,
		`SELECT id, session_id, time_created, data FROM message WHERE session_id = ? ORDER BY time_created, id`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query opencode source messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []opencodeSourceMessage
	for rows.Next() {
		var message opencodeSourceMessage
		if err := rows.Scan(&message.ID, &message.SessionID, &message.TimeCreatedMs, &message.Data); err != nil {
			return nil, fmt.Errorf("scan opencode source message: %w", err)
		}
		out = append(out, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate opencode source messages: %w", err)
	}
	return out, nil
}

func loadOpencodeSourceParts(ctx context.Context, querier opencodeSourceQuerier, sessionID string) ([]opencodeSourcePart, error) {
	rows, err := querier.QueryContext(ctx,
		`SELECT id, message_id, session_id, time_created, data FROM part WHERE session_id = ? ORDER BY message_id, id`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query opencode source parts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []opencodeSourcePart
	for rows.Next() {
		var part opencodeSourcePart
		if err := rows.Scan(&part.ID, &part.MessageID, &part.SessionID, &part.TimeCreatedMs, &part.Data); err != nil {
			return nil, fmt.Errorf("scan opencode source part: %w", err)
		}
		out = append(out, part)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate opencode source parts: %w", err)
	}
	return out, nil
}

func loadOpencodeSourceInputs(ctx context.Context, querier opencodeSourceQuerier, sessionID string, exists bool) ([]opencodeSourceInput, error) {
	if !exists {
		return nil, nil
	}
	rows, err := querier.QueryContext(ctx,
		`SELECT id, session_id, prompt, time_created FROM session_input WHERE session_id = ? ORDER BY time_created, id`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query opencode source session_input: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []opencodeSourceInput
	for rows.Next() {
		var input opencodeSourceInput
		if err := rows.Scan(&input.ID, &input.SessionID, &input.Prompt, &input.TimeCreatedMs); err != nil {
			return nil, fmt.Errorf("scan opencode source session_input: %w", err)
		}
		out = append(out, input)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate opencode source session_input: %w", err)
	}
	return out, nil
}

func loadOpencodeSourceSessionMessages(ctx context.Context, querier opencodeSourceQuerier, sessionID string, exists bool, hasSeq bool) ([]opencodeSourceSessionMessage, error) {
	if !exists {
		return nil, nil
	}
	seqColumn := "NULL AS seq"
	orderBy := "time_created, id"
	if hasSeq {
		seqColumn = "seq"
		orderBy = "seq, id"
	}
	query := fmt.Sprintf(`SELECT id, session_id, type, %s, time_created, data FROM session_message WHERE session_id = ? ORDER BY %s`, seqColumn, orderBy)
	rows, err := querier.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query opencode source session_message: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []opencodeSourceSessionMessage
	for rows.Next() {
		var message opencodeSourceSessionMessage
		var seq sql.NullInt64
		if err := rows.Scan(&message.ID, &message.SessionID, &message.Type, &seq, &message.TimeCreatedMs, &message.Data); err != nil {
			return nil, fmt.Errorf("scan opencode source session_message: %w", err)
		}
		if seq.Valid {
			message.Seq = seq.Int64
		}
		out = append(out, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate opencode source session_message: %w", err)
	}
	return out, nil
}

func opencodeSourceTableExists(ctx context.Context, querier opencodeSourceQuerier, table string) (bool, error) {
	var found int
	err := querier.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check opencode source table %s: %w", table, err)
	}
	return found == 1, nil
}

func beginOpencodeSourceReadSnapshot(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin opencode source read snapshot: %w", err)
	}
	if err := pinOpencodeSourceReadSnapshot(ctx, tx); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func pinOpencodeSourceReadSnapshot(ctx context.Context, tx *sql.Tx) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master`).Scan(&count); err != nil {
		return fmt.Errorf("pin opencode source read snapshot: %w", err)
	}
	return nil
}

func (s *opencodeSourceState) extract() error {
	if err := s.prepareSchema(); err != nil {
		return err
	}
	return s.streamSessions(func(session opencodeSourceSession) error {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		if err := s.loadSessionRows(session.ID); err != nil {
			return err
		}
		defer s.clearSessionRows()
		artifact, err := s.sessionBoundary(session)
		if err != nil {
			return err
		}
		if err := s.emit(artifact); err != nil {
			return err
		}
		metadata, ok, err := s.opencodeSessionMetadata(session)
		if err != nil {
			return err
		}
		if ok {
			if err := s.emit(metadata); err != nil {
				return err
			}
		}
		if err := s.extractSessionMessages(session); err != nil {
			return err
		}
		if err := s.extractTurns(session); err != nil {
			return err
		}
		return nil
	})
}

func opencodeSourceSelector(kind string, id string) string {
	values := url.Values{}
	values.Set("table", kind)
	values.Set("id", id)
	return opencodeSQLiteScheme + "://?" + values.Encode()
}

func opencodeRecoverableJSONError(err error) bool {
	var syntaxErr *json.SyntaxError
	return errors.As(err, &syntaxErr)
}
