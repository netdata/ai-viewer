package parity

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

type opencodeCompactionEventIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	TurnSeq         int64  `json:"turn_seq"`
	OpSeq           int64  `json:"op_seq,omitempty"`
	Auto            bool   `json:"auto"`
	Timestamp       int64  `json:"timestamp"`
	Severity        string `json:"severity"`
	Message         string `json:"message"`
}

type opencodeAttachmentMetadataIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	TurnSeq         int64  `json:"turn_seq"`
	OpSeq           int64  `json:"op_seq,omitempty"`
	Filename        string `json:"filename,omitempty"`
	URL             string `json:"url,omitempty"`
	MIME            string `json:"mime,omitempty"`
}

type opencodePatchMetadataIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	TurnSeq         int64  `json:"turn_seq"`
	OpSeq           int64  `json:"op_seq,omitempty"`
	PartID          string `json:"part_id"`
	Hash            string `json:"hash,omitempty"`
	FilesCount      int64  `json:"files_count"`
	FilesSHA256     string `json:"files_sha256"`
}

type opencodeAssistantErrorIdentity struct {
	NativeSessionID    string `json:"native_session_id"`
	TurnSeq            int64  `json:"turn_seq"`
	ErrorClass         string `json:"error_class"`
	ErrorMessageSHA256 string `json:"error_message_sha256"`
	Timestamp          int64  `json:"timestamp"`
}

type opencodeSessionMessageIdentity struct {
	NativeSessionID  string `json:"native_session_id"`
	SessionMessageID string `json:"session_message_id"`
	EventType        string `json:"event_type"`
	Seq              int64  `json:"seq,omitempty"`
	Timestamp        int64  `json:"timestamp"`
	Severity         string `json:"severity"`
	Message          string `json:"message"`
	Agent            string `json:"agent,omitempty"`
	ModelID          string `json:"model_id,omitempty"`
	ProviderID       string `json:"provider_id,omitempty"`
	Variant          string `json:"variant,omitempty"`
	DataSHA256       string `json:"data_sha256"`
}

type opencodeSessionMessageData struct {
	Agent string               `json:"agent"`
	Model opencodeSessionModel `json:"model"`
}

func (s *opencodeSourceState) sessionBoundary(session opencodeSourceSession) (Artifact, error) {
	kind := "root"
	if session.ParentID != "" {
		kind = "sub_agent"
	}
	status := "running"
	var endedAt *int64
	if session.TimeArchivedMs.Valid {
		status = "completed"
		endedAt = ptrInt64(opencodeMsToUS(session.TimeArchivedMs.Int64))
	} else {
		errorEndUS, ok, err := s.opencodeLastAssistantErrorEndUS(session.ID)
		if err != nil {
			return Artifact{}, err
		}
		if ok {
			status = "failed"
			endedAt = ptrInt64(errorEndUS)
		}
	}
	rootNativeID, err := s.rootNativeID(session.ID)
	if err != nil {
		return Artifact{}, err
	}
	identity := sessionBoundaryIdentity{
		NativeSessionID:       session.ID,
		ParentNativeSessionID: session.ParentID,
		RootNativeSessionID:   rootNativeID,
		Kind:                  kind,
		Status:                status,
		StartedAt:             opencodeMsToUS(session.TimeCreatedMs),
		EndedAt:               endedAt,
	}
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "opencode",
		sourceFile:       s.dbPath,
		nativeSessionID:  session.ID,
		nativeArtifactID: "session:" + session.ID,
		class:            ClassSessionBoundary,
		selectorURI:      opencodeSourceSelector("session", session.ID),
		identity:         identity,
	})
}

func (s *opencodeSourceState) extractSessionMessages(session opencodeSourceSession) error {
	for _, message := range s.sessionMessagesBySession[session.ID] {
		identity, ok, err := opencodeSessionMessageIdentityFromSource(message)
		if err != nil {
			if opencodeRecoverableJSONError(err) {
				if err := s.emit(opencodeSourceCorruptionArtifact(
					s.sourceID,
					s.dbPath,
					session.ID,
					"",
					"session_message",
					message.ID,
					"data",
					"valid opencode session_message JSON",
					message.Data,
				)); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if !ok {
			continue
		}
		if err := s.emit(opencodeSessionMessageLogArtifact(s.sourceID, s.dbPath, session.ID, message, identity.Message)); err != nil {
			return err
		}
		artifact, err := identityArtifact(identityArtifactInput{
			sourceID:         s.sourceID,
			adapter:          opencodeFormat,
			sourceFile:       s.dbPath,
			nativeSessionID:  session.ID,
			nativeArtifactID: "session_message:" + message.ID + ":system_op",
			class:            ClassSystemOp,
			selectorURI:      opencodeSourceSelector("session_message", message.ID),
			identity:         identity,
		})
		if err != nil {
			return err
		}
		if err := s.emit(artifact); err != nil {
			return err
		}
	}
	return nil
}

func opencodeSessionMessageIdentityFromSource(message opencodeSourceSessionMessage) (opencodeSessionMessageIdentity, bool, error) {
	logMessage, ok := opencodeSessionMessageLogMessage(message.Type)
	if !ok {
		return opencodeSessionMessageIdentity{}, false, fmt.Errorf("unknown opencode session_message type %q in row %s", message.Type, message.ID)
	}
	data, err := decodeOpencodeSessionMessageData(message.Data)
	if err != nil {
		return opencodeSessionMessageIdentity{}, false, err
	}
	hash, err := opencodeCanonicalJSONSHA256(message.Data)
	if err != nil {
		return opencodeSessionMessageIdentity{}, false, err
	}
	identity := opencodeSessionMessageIdentity{
		NativeSessionID:  message.SessionID,
		SessionMessageID: message.ID,
		EventType:        message.Type,
		Seq:              message.Seq,
		Timestamp:        opencodeMsToUS(message.TimeCreatedMs),
		Severity:         "INF",
		Message:          logMessage,
		Agent:            data.Agent,
		ModelID:          data.Model.modelID(),
		ProviderID:       data.Model.ProviderID,
		Variant:          data.Model.Variant,
		DataSHA256:       hash,
	}
	return identity, true, nil
}

func decodeOpencodeSessionMessageData(raw []byte) (opencodeSessionMessageData, error) {
	var data opencodeSessionMessageData
	if err := json.Unmarshal(raw, &data); err != nil {
		return opencodeSessionMessageData{}, fmt.Errorf("decode opencode session_message data: %w", err)
	}
	return data, nil
}

func opencodeCanonicalJSONSHA256(raw []byte) (string, error) {
	var value interface{}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("decode opencode canonical JSON: %w", err)
	}
	body, err := canonicalIdentityBytes(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum), nil
}

func opencodeSessionMessageLogArtifact(sourceID, dbPath, sessionID string, message opencodeSourceSessionMessage, logMessage string) Artifact {
	messageBytes := []byte(logMessage)
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          opencodeFormat,
		SourceID:         sourceID,
		SourceFile:       dbPath,
		NativeSessionID:  sessionID,
		NativeArtifactID: "session_message:" + message.ID + ":log",
		Class:            ClassLogEntry,
		Availability:     logAvailability(messageBytes),
		HashDomain:       HashSemanticText,
		Selector: Selector{
			URI: opencodeSourceSelector("session_message", message.ID),
		},
		Bytes:          int64(len(messageBytes)),
		Chars:          int64(utf8.RuneCount(messageBytes)),
		ComputedSHA256: stringSHA256(logMessage),
	}
}

func opencodeSessionMessageLogMessage(typ string) (string, bool) {
	switch typ {
	case "agent-switched":
		return "session agent switched", true
	case "model-switched":
		return "session model switched", true
	default:
		return "", false
	}
}

func (s *opencodeSourceState) extractTurns(session opencodeSourceSession) error {
	var turnSeq int64
	userByID := map[string]opencodeSourceUserPrompt{}
	consumedUsers := map[string]struct{}{}
	var pendingUser *opencodeSourceUserPrompt
	for _, message := range s.messagesBySession[session.ID] {
		data, err := decodeOpencodeSourceMessage(message.Data)
		if err != nil {
			if opencodeRecoverableJSONError(err) {
				if err := s.emit(opencodeSourceCorruptionArtifact(
					s.sourceID,
					s.dbPath,
					message.SessionID,
					"",
					"message",
					message.ID,
					"data",
					"valid opencode message JSON",
					message.Data,
				)); err != nil {
					return err
				}
				continue
			}
			return err
		}
		switch data.Role {
		case "user":
			user := opencodeSourceUserPrompt{message: message}
			if input, ok := s.inputsByID[message.ID]; ok {
				user.input = &input
			}
			userByID[message.ID] = user
			pendingUser = &user
			continue
		case "assistant":
		default:
			return fmt.Errorf("unknown opencode message role %q in row %s", data.Role, message.ID)
		}
		var user *opencodeSourceUserPrompt
		if data.ParentID != "" {
			if _, consumed := consumedUsers[data.ParentID]; !consumed {
				if matched, ok := userByID[data.ParentID]; ok {
					user = &matched
					consumedUsers[data.ParentID] = struct{}{}
					if pendingUser != nil && pendingUser.message.ID == data.ParentID {
						pendingUser = nil
					}
				}
			}
		} else {
			user = pendingUser
			if user != nil {
				consumedUsers[user.message.ID] = struct{}{}
				pendingUser = nil
			}
		}
		turnSeq++
		parts := s.partsByMessage[message.ID]
		turnArtifact, err := s.turnBoundary(session.ID, message, data, parts, turnSeq)
		if err != nil {
			return err
		}
		if err := s.emit(turnArtifact); err != nil {
			return err
		}
		if data.Error != nil {
			if err := s.appendOpencodeAssistantError(session.ID, message, data, turnSeq); err != nil {
				return err
			}
		}
		if err := s.extractParts(parts, opencodeSourceTurnScope{
			sessionID: session.ID,
			turnSeq:   turnSeq,
			modelID:   data.ModelID,
		}, user); err != nil {
			return err
		}
	}
	return nil
}

func (s *opencodeSourceState) opencodeLastAssistantErrorEndUS(sessionID string) (int64, bool, error) {
	var errorEndUS int64
	var hasError bool
	for _, message := range s.messagesBySession[sessionID] {
		data, err := decodeOpencodeSourceMessage(message.Data)
		if err != nil {
			if opencodeRecoverableJSONError(err) {
				continue
			}
			return 0, false, err
		}
		if data.Role != "assistant" {
			continue
		}
		if data.Error != nil {
			errorEndUS = opencodeTurnEndUS(message, data)
			hasError = true
			continue
		}
		errorEndUS = 0
		hasError = false
	}
	return errorEndUS, hasError, nil
}

type opencodeSourceUserPrompt struct {
	message opencodeSourceMessage
	input   *opencodeSourceInput
}

func (s *opencodeSourceState) turnBoundary(sessionID string, message opencodeSourceMessage, data opencodeSourceMessageData, parts []opencodeSourcePart, turnSeq int64) (Artifact, error) {
	status := "running"
	var endedAt *int64
	if opencodeTurnTerminal(data, parts) {
		status = "completed"
		if data.Error != nil {
			status = "failed"
		}
		endedAt = ptrInt64(opencodeTurnEndUS(message, data))
	}
	identity := turnBoundaryIdentity{
		NativeSessionID: sessionID,
		TurnSeq:         turnSeq,
		Status:          status,
		StartedAt:       opencodeMsToUS(message.TimeCreatedMs),
		EndedAt:         endedAt,
	}
	nativeTurnID := opencodeNativeTurnID(turnSeq)
	return identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "opencode",
		sourceFile:       s.dbPath,
		nativeSessionID:  sessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeTurnID,
		class:            ClassTurnBoundary,
		selectorURI:      opencodeSourceSelector("message", message.ID),
		identity:         identity,
	})
}

func (s *opencodeSourceState) extractParts(parts []opencodeSourcePart, scope opencodeSourceTurnScope, user *opencodeSourceUserPrompt) error {
	state := &opencodeSourceTurnState{
		scope: scope,
		ops:   map[int64]opencodeSourceOp{},
	}
	if user != nil {
		if err := s.recordUserPrompt(state, *user); err != nil {
			return err
		}
	}
	for _, part := range parts {
		data, err := decodeOpencodeSourcePart(part.Data)
		if err != nil {
			if opencodeRecoverableJSONError(err) {
				if err := s.emit(opencodeSourceCorruptionArtifact(
					s.sourceID,
					s.dbPath,
					part.SessionID,
					opencodeNativeTurnID(scope.turnSeq),
					"part",
					part.ID,
					"data",
					"valid opencode part JSON",
					part.Data,
				)); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if err := s.extractPart(state, part, data); err != nil {
			return err
		}
	}
	for seq := int64(1); seq <= state.opSeq; seq++ {
		op, ok := state.ops[seq]
		if !ok {
			continue
		}
		if err := s.appendOpArtifacts(state.scope, seq, op); err != nil {
			return err
		}
	}
	return nil
}

func (s *opencodeSourceState) extractPart(state *opencodeSourceTurnState, part opencodeSourcePart, data opencodeSourcePartData) error {
	switch data.Type {
	case "step-start":
		state.openLLM(part.TimeCreatedMs)
	case "step-finish":
		state.closeLLM(part.TimeCreatedMs)
	case "reasoning":
		return s.recordReasoning(state, part, data)
	case "text":
		return s.recordAssistantText(state, part, data)
	case "tool":
		return s.recordTool(state, part, data)
	case "compaction":
		return s.recordCompaction(state, part, data)
	case "retry":
		return s.recordRetryLog(state, part, data)
	case "file":
		return s.recordFileAttachment(state, part, data)
	case "patch":
		return s.recordPatchMetadata(state, part, data)
	default:
		return fmt.Errorf("unknown opencode part type %q in row %s", data.Type, part.ID)
	}
	return nil
}

func (s *opencodeSourceState) appendOpArtifacts(scope opencodeSourceTurnScope, seq int64, op opencodeSourceOp) error {
	nativeTurnID := opencodeNativeTurnID(scope.turnSeq)
	nativeOpID := opencodeNativeOpID(scope.turnSeq, seq)
	identity := opBoundaryIdentity{
		NativeSessionID: scope.sessionID,
		TurnSeq:         scope.turnSeq,
		OpSeq:           seq,
		Kind:            op.kind,
		Name:            op.name,
		ToolNamespace:   op.toolNamespace,
		Status:          op.status,
		StartedAt:       op.startedAt,
		EndedAt:         op.endedAt,
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "opencode",
		sourceFile:       s.dbPath,
		nativeSessionID:  scope.sessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: nativeOpID,
		class:            ClassOpBoundary,
		selectorURI:      opencodeSourceSelector("op", nativeOpID),
		identity:         identity,
	})
	if err != nil {
		return err
	}
	if err := s.emit(artifact); err != nil {
		return err
	}
	if op.errorClass != "" || op.errorMessage != "" {
		if err := s.appendToolError(scope, seq, op); err != nil {
			return err
		}
	}
	if op.childID != "" {
		if err := s.appendSubagentLink(scope, seq, op.childID); err != nil {
			return err
		}
	}
	return nil
}

func (s *opencodeSourceState) appendToolError(scope opencodeSourceTurnScope, seq int64, op opencodeSourceOp) error {
	nativeOpID := opencodeNativeOpID(scope.turnSeq, seq)
	identity := opErrorIdentity{
		NativeSessionID:    scope.sessionID,
		TurnSeq:            scope.turnSeq,
		OpSeq:              seq,
		OpKind:             op.kind,
		ErrorClass:         op.errorClass,
		ErrorMessageSHA256: stringSHA256(op.errorMessage),
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "opencode",
		sourceFile:       s.dbPath,
		nativeSessionID:  scope.sessionID,
		nativeTurnID:     opencodeNativeTurnID(scope.turnSeq),
		nativeArtifactID: nativeOpID + ":error",
		class:            ClassToolError,
		selectorURI:      opencodeSourceSelector("op", nativeOpID) + "#error",
		identity:         identity,
	})
	if err != nil {
		return err
	}
	return s.emit(artifact)
}

func (s *opencodeSourceState) appendOpencodeAssistantError(sessionID string, message opencodeSourceMessage, data opencodeSourceMessageData, turnSeq int64) error {
	nativeTurnID := opencodeNativeTurnID(turnSeq)
	identity := opencodeAssistantErrorIdentity{
		NativeSessionID:    sessionID,
		TurnSeq:            turnSeq,
		ErrorClass:         opencodeAssistantErrorClass(data.Error),
		ErrorMessageSHA256: stringSHA256(opencodeAssistantErrorMessage(data.Error)),
		Timestamp:          opencodeTurnEndUS(message, data),
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          opencodeFormat,
		sourceFile:       s.dbPath,
		nativeSessionID:  sessionID,
		nativeTurnID:     nativeTurnID,
		nativeArtifactID: opencodeAssistantErrorNativeID(turnSeq),
		class:            ClassLLMError,
		selectorURI:      opencodeSourceSelector("message", message.ID) + "#error",
		identity:         identity,
	})
	if err != nil {
		return err
	}
	return s.emit(artifact)
}

func (s *opencodeSourceState) appendSubagentLink(scope opencodeSourceTurnScope, seq int64, childID string) error {
	nativeOpID := opencodeNativeOpID(scope.turnSeq, seq)
	identity := subagentLinkIdentity{
		ParentNativeSessionID: scope.sessionID,
		ParentTurnSeq:         scope.turnSeq,
		ParentOpSeq:           seq,
		ChildNativeSessionID:  childID,
		LinkKind:              "child_session",
		Direction:             "parent_to_child",
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "opencode",
		sourceFile:       s.dbPath,
		nativeSessionID:  scope.sessionID,
		nativeTurnID:     opencodeNativeTurnID(scope.turnSeq),
		nativeArtifactID: nativeOpID + ":child_session:" + childID,
		class:            ClassSubagentLink,
		selectorURI:      opencodeSourceSelector("op", nativeOpID) + "#child_session",
		identity:         identity,
	})
	if err != nil {
		return err
	}
	return s.emit(artifact)
}

func (s *opencodeSourceState) appendOpencodeCompactionEvent(scope opencodeSourceTurnScope, opSeq int64, part opencodeSourcePart, data opencodeSourcePartData) error {
	message := opencodeCompactionLogMessage(data.Auto)
	identity := opencodeCompactionEventIdentity{
		NativeSessionID: scope.sessionID,
		TurnSeq:         scope.turnSeq,
		OpSeq:           opSeq,
		Auto:            data.Auto,
		Timestamp:       opencodeMsToUS(part.TimeCreatedMs),
		Severity:        "INF",
		Message:         message,
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "opencode",
		sourceFile:       s.dbPath,
		nativeSessionID:  scope.sessionID,
		nativeTurnID:     opencodeNativeTurnID(scope.turnSeq),
		nativeArtifactID: opencodePartNativeID(part.ID, "compaction"),
		class:            ClassCompactionEvent,
		selectorURI:      opencodeSourceSelector("part", part.ID),
		identity:         identity,
	})
	if err != nil {
		return err
	}
	return s.emit(artifact)
}

func (s *opencodeSourceState) appendOpencodeAttachmentMetadata(scope opencodeSourceTurnScope, opSeq int64, part opencodeSourcePart, data opencodeSourcePartData) error {
	identity := opencodeAttachmentMetadataIdentity{
		NativeSessionID: scope.sessionID,
		TurnSeq:         scope.turnSeq,
		OpSeq:           opSeq,
		Filename:        data.Filename,
		URL:             data.URL,
		MIME:            data.MIME,
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "opencode",
		sourceFile:       s.dbPath,
		nativeSessionID:  scope.sessionID,
		nativeTurnID:     opencodeNativeTurnID(scope.turnSeq),
		nativeArtifactID: opencodePartNativeID(part.ID, "file"),
		class:            ClassAttachmentMetadata,
		selectorURI:      opencodeSourceSelector("part", part.ID),
		identity:         identity,
	})
	if err != nil {
		return err
	}
	return s.emit(artifact)
}

func (s *opencodeSourceState) appendOpencodePatchMetadata(scope opencodeSourceTurnScope, opSeq int64, part opencodeSourcePart, data opencodeSourcePartData) error {
	identity := opencodePatchMetadataIdentity{
		NativeSessionID: scope.sessionID,
		TurnSeq:         scope.turnSeq,
		OpSeq:           opSeq,
		PartID:          part.ID,
		Hash:            data.Hash,
		FilesCount:      int64(len(data.Files)),
		FilesSHA256:     opencodePatchFilesSHA256(data.Files),
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:         s.sourceID,
		adapter:          "opencode",
		sourceFile:       s.dbPath,
		nativeSessionID:  scope.sessionID,
		nativeTurnID:     opencodeNativeTurnID(scope.turnSeq),
		nativeArtifactID: opencodePartNativeID(part.ID, "patch"),
		class:            ClassPatchMetadata,
		selectorURI:      opencodeSourceSelector("part", part.ID),
		identity:         identity,
	})
	if err != nil {
		return err
	}
	return s.emit(artifact)
}

func (s *opencodeSourceState) appendOpencodeLogEntry(scope opencodeSourceTurnScope, opSeq int64, part opencodeSourcePart, severity string, message string) error {
	tsUs := opencodeMsToUS(part.TimeCreatedMs)
	nativeID := logNativeArtifactID(opencodeLogScope(scope.turnSeq, opSeq), tsUs, severity, "opencode", message)
	messageBytes := []byte(message)
	return s.emit(Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "opencode",
		SourceID:         s.sourceID,
		SourceFile:       s.dbPath,
		NativeSessionID:  scope.sessionID,
		NativeTurnID:     opencodeNativeTurnID(scope.turnSeq),
		NativeArtifactID: nativeID,
		Class:            ClassLogEntry,
		Availability:     logAvailability(messageBytes),
		HashDomain:       HashSemanticText,
		Selector: Selector{
			URI: opencodeSourceSelector("part", part.ID),
		},
		Bytes:          int64(len(messageBytes)),
		Chars:          int64(utf8.RuneCount(messageBytes)),
		ComputedSHA256: stringSHA256(message),
	})
}

func opencodePartNativeID(partID string, suffix string) string {
	return "part:" + partID + ":" + suffix
}

func opencodePatchFilesSHA256(files []string) string {
	if files == nil {
		files = []string{}
	}
	body, _ := json.Marshal(files)
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum)
}

func opencodeCompactionLogMessage(auto bool) string {
	return fmt.Sprintf("session compacted (auto=%t)", auto)
}

func opencodeRetryLogMessage(data opencodeSourcePartData) string {
	message := fmt.Sprintf("API retry attempt %d", data.Attempt)
	if data.Error.Name != "" {
		message += ": " + data.Error.Name
	}
	return message
}

func opencodeLogScope(turnSeq int64, opSeq int64) string {
	if turnSeq > 0 && opSeq > 0 {
		return fmt.Sprintf("op:%d:%d", turnSeq, opSeq)
	}
	if turnSeq > 0 {
		return fmt.Sprintf("turn:%d", turnSeq)
	}
	return "source"
}

func opencodePayloadArtifact(sourceID, dbPath string, scope opencodeSourceTurnScope, part opencodeSourcePart, class ArtifactClass, field string) (Artifact, error) {
	resolved, err := resolveOpencodePayloadField(part.Data, field)
	if err != nil {
		return Artifact{}, err
	}
	sum := sha256.Sum256(resolved.bytes)
	availability := AvailabilityAvailable
	if len(resolved.bytes) == 0 {
		availability = AvailabilitySourceEmpty
	}
	chars := int64(-1)
	if resolved.hashDomain == HashSemanticText && utf8.Valid(resolved.bytes) {
		chars = int64(utf8.RuneCount(resolved.bytes))
	}
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "opencode",
		SourceID:         sourceID,
		SourceFile:       dbPath,
		NativeSessionID:  scope.sessionID,
		NativeTurnID:     opencodeNativeTurnID(scope.turnSeq),
		NativeArtifactID: opencodePayloadNativeID(part.ID, field),
		Class:            class,
		Availability:     availability,
		HashDomain:       resolved.hashDomain,
		Selector:         opencodePayloadSelector(part.ID, field),
		Bytes:            int64(len(resolved.bytes)),
		Chars:            chars,
		ComputedSHA256:   fmt.Sprintf("%x", sum),
	}, nil
}

func opencodeSourceCorruptionArtifact(sourceID, dbPath string, nativeSessionID string, nativeTurnID string, table string, rowID string, field string, expected string, raw []byte) Artifact {
	sum := sha256.Sum256(raw)
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          opencodeFormat,
		SourceID:         sourceID,
		SourceFile:       dbPath,
		NativeSessionID:  nativeSessionID,
		NativeTurnID:     nativeTurnID,
		NativeArtifactID: fmt.Sprintf("source_corruption:%s:%s:%s", table, rowID, field),
		Class:            ClassSourceCorruption,
		Availability:     AvailabilitySourceCorrupt,
		HashDomain:       HashRawBytes,
		Selector: Selector{
			URI:       opencodeSourceSelector(table, rowID),
			FieldPath: field,
		},
		Bytes:          int64(len(raw)),
		Chars:          -1,
		ComputedSHA256: fmt.Sprintf("%x", sum),
		IntegrityFailures: []IntegrityFailure{{
			Field:    field,
			Expected: expected,
			Actual:   "decode_error",
		}},
	}
}

func opencodeInputPayloadArtifact(sourceID, dbPath string, scope opencodeSourceTurnScope, input opencodeSourceInput, class ArtifactClass, field string) (Artifact, error) {
	resolved, err := resolveOpencodeInputPayloadField(input.Prompt, field)
	if err != nil {
		return Artifact{}, err
	}
	sum := sha256.Sum256(resolved.bytes)
	availability := AvailabilityAvailable
	if len(resolved.bytes) == 0 {
		availability = AvailabilitySourceEmpty
	}
	chars := int64(-1)
	if resolved.hashDomain == HashSemanticText && utf8.Valid(resolved.bytes) {
		chars = int64(utf8.RuneCount(resolved.bytes))
	}
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "opencode",
		SourceID:         sourceID,
		SourceFile:       dbPath,
		NativeSessionID:  scope.sessionID,
		NativeTurnID:     opencodeNativeTurnID(scope.turnSeq),
		NativeArtifactID: opencodeInputPayloadNativeID(input.ID, field),
		Class:            class,
		Availability:     availability,
		HashDomain:       resolved.hashDomain,
		Selector:         opencodeInputPayloadSelector(input.ID, field),
		Bytes:            int64(len(resolved.bytes)),
		Chars:            chars,
		ComputedSHA256:   fmt.Sprintf("%x", sum),
	}, nil
}

func opencodeSourceUnavailablePayload(sourceID, dbPath string, scope opencodeSourceTurnScope, opSeq int64, class ArtifactClass, kind string) Artifact {
	return Artifact{
		SchemaVersion:    SchemaVersion,
		Adapter:          "opencode",
		SourceID:         sourceID,
		SourceFile:       dbPath,
		NativeSessionID:  scope.sessionID,
		NativeTurnID:     opencodeNativeTurnID(scope.turnSeq),
		NativeArtifactID: opPayloadNativeID(scope.turnSeq, opSeq, kind, 1),
		Class:            class,
		Availability:     AvailabilitySourceUnavailable,
	}
}

func decodeOpencodeSourceMessage(raw []byte) (opencodeSourceMessageData, error) {
	var data opencodeSourceMessageData
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, fmt.Errorf("decode opencode source message.data: %w", err)
	}
	return data, nil
}

func decodeOpencodeSourcePart(raw []byte) (opencodeSourcePartData, error) {
	var data opencodeSourcePartData
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, fmt.Errorf("decode opencode source part.data: %w", err)
	}
	return data, nil
}

func opencodeToolNameNamespace(tool string) (name, namespace string) {
	if i := strings.IndexByte(tool, '_'); i > 0 && i < len(tool)-1 {
		return tool[i+1:], tool[:i]
	}
	return tool, ""
}
