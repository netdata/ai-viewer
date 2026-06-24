package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/netdata/ai-viewer/internal/canonical"
)

func (m *sessionMapper) emitUserPrompt(tc *turnContext, user *userMessageContext) []canonical.Event {
	tc.opSeq++
	seq := tc.opSeq
	tsUs := m.msToMicrosWarn(user.message.TimeCreatedMs, "message.time_created (user prompt)")
	locationURI := ""
	originalBytes := int64(-1)
	if user.input != nil {
		text, ok, err := sessionInputPromptText(user.input.Prompt)
		if err != nil {
			m.mwarn(fmt.Errorf("opencode: undecodable session_input.prompt (table=session_input id=%s); user prompt bytes unavailable: %w", user.input.ID, err))
		} else if ok {
			locationURI = buildInputPayloadURI(user.input.ID, "prompt.text")
			originalBytes = int64(len(text))
		}
	}
	events := []canonical.Event{
		canonical.OpStartedEvent{
			EventBase:       m.nextBase(tsUs),
			SessionNativeID: m.nativeID(),
			TurnSeq:         tc.turnSeq,
			Seq:             seq,
			ParentOpSeq:     -1,
			Kind:            canonical.OpInternal,
			Name:            "user_input",
		},
		canonical.OpFinalizedEvent{
			EventBase:       m.nextBase(tsUs),
			SessionNativeID: m.nativeID(),
			TurnSeq:         tc.turnSeq,
			Seq:             seq,
			Status:          "completed",
			EndTs:           tsUs,
		},
		canonical.PayloadRefEvent{
			EventBase:       m.nextBase(tsUs),
			SessionNativeID: m.nativeID(),
			TurnSeq:         tc.turnSeq,
			OpSeq:           seq,
			PayloadKind:     "tool_request",
			Format:          "text",
			LocationURI:     locationURI,
			OriginalBytes:   originalBytes,
		},
	}
	if user.input != nil {
		imageRefs, err := sessionInputImageFiles(user.input.Prompt)
		if err != nil {
			m.mwarn(fmt.Errorf("opencode: undecodable session_input.prompt files (table=session_input id=%s); user image bytes unavailable: %w", user.input.ID, err))
		} else {
			for _, image := range imageRefs {
				events = append(events, canonical.PayloadRefEvent{
					EventBase:       m.nextBase(tsUs),
					SessionNativeID: m.nativeID(),
					TurnSeq:         tc.turnSeq,
					OpSeq:           seq,
					PayloadKind:     "tool_request",
					Format:          "json",
					LocationURI:     buildInputPayloadURI(user.input.ID, image.field),
					OriginalBytes:   int64(len(image.canonical)),
				})
			}
		}
	}
	return events
}

type sessionInputImageFile struct {
	field     string
	canonical []byte
}

func sessionInputPromptText(raw []byte) (string, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false, errEmptyData
	}
	var prompt struct {
		Text *string `json:"text"`
	}
	if err := json.Unmarshal(raw, &prompt); err != nil {
		return "", false, err
	}
	if prompt.Text == nil {
		return "", false, nil
	}
	return *prompt.Text, true, nil
}

func sessionInputImageFiles(raw []byte) ([]sessionInputImageFile, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errEmptyData
	}
	var prompt struct {
		Files []json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(raw, &prompt); err != nil {
		return nil, err
	}
	out := make([]sessionInputImageFile, 0, len(prompt.Files))
	for i, file := range prompt.Files {
		var meta struct {
			MIME string `json:"mime"`
		}
		if err := json.Unmarshal(file, &meta); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(strings.ToLower(meta.MIME), "image/") {
			continue
		}
		canonical, err := sessionInputCanonicalJSON(file)
		if err != nil {
			return nil, err
		}
		out = append(out, sessionInputImageFile{
			field:     fmt.Sprintf("prompt.files.%d", i),
			canonical: canonical,
		})
	}
	return out, nil
}

func sessionInputCanonicalJSON(raw []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
