package opencode

import (
	"encoding/json"
	"testing"

	"github.com/netdata/ai-viewer/internal/canonical"
)

// This file pins the SOW-0005 ROUND-5 external-review fixes that live in the PURE
// mapper layer:
//   - P3-1: a failed SessionFinalizedEvent carries ErrorMessage from
//     data.error.data.message (opencode's AssistantError serializes as
//     {name, data:{message,...}}); a message-less / malformed / absent error data
//     degrades to an empty ErrorMessage WITHOUT aborting the session.
//
// (P2-1's no-emission-under-open-tx and P2-2's required-ownership-id-columns fixes
// are store-layer concerns pinned in review_round5_store_test.go; P3-1's golden is
// the i_failed_assistant scenario + TestGoldenInvariant_IFailedAssistant.)

// asgMsgErrData builds an assistant messageRow whose data.error is the full
// opencode tagged shape {"name":..,"data":..}. errData is marshalled verbatim as
// the error's `data` body so a test can exercise the {message}, message-less, and
// malformed branches. A nil errData omits the data key entirely (error with only
// a name). completedMs is set so the turn is terminal (and the session finalizes).
func asgMsgErrData(t *testing.T, id, name string, errData any) messageRow {
	t.Helper()
	completed := int64(2000)
	errObj := map[string]any{"name": name}
	if errData != nil {
		errObj["data"] = errData
	}
	d := map[string]any{
		"role":       "assistant",
		"providerID": "the-alias",
		"modelID":    "the-model",
		"agent":      "test-agent",
		"cost":       0.1,
		"tokens":     tokenCounts{Input: 10},
		"time":       map[string]any{"created": 1500, "completed": completed},
		"finish":     "error",
		"error":      errObj,
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal assistant error msg: %v", err)
	}
	return messageRow{ID: id, SessionID: "ses_x", TimeCreatedMs: 1500, TimeUpdatedMs: 1500, Data: raw}
}

// TestP3_1_SessionErrorMessageFromData pins that a failed session terminal carries
// BOTH ErrorClass (data.error.name) AND ErrorMessage (data.error.data.message).
func TestP3_1_SessionErrorMessageFromData(t *testing.T) {
	s := rootSession("ses_err", 0)
	msgs := []messageWithParts{
		mwp(asgMsgErrData(t, "msg_a", "MessageAbortedError",
			map[string]any{"message": "request was aborted by the user"})),
	}
	evs := run(t, s, msgs)
	fins := finalizes(evs)
	if len(fins) != 1 {
		t.Fatalf("SessionFinalized count = %d, want 1", len(fins))
	}
	if fins[0].Status != canonical.StatusFailed {
		t.Fatalf("Status = %q, want failed", fins[0].Status)
	}
	if fins[0].ErrorClass != "MessageAbortedError" {
		t.Errorf("ErrorClass = %q, want MessageAbortedError", fins[0].ErrorClass)
	}
	if fins[0].ErrorMessage != "request was aborted by the user" {
		t.Errorf("ErrorMessage = %q, want the data.message string (P3-1)", fins[0].ErrorMessage)
	}
}

// TestP3_1_SessionErrorMessageDegrades pins that the message-less / malformed /
// absent error-data shapes leave ErrorMessage EMPTY while still finalizing failed
// with the correct ErrorClass — the decode is best-effort and never aborts.
func TestP3_1_SessionErrorMessageDegrades(t *testing.T) {
	cases := []struct {
		name     string
		errName  string
		errData  any
		wantMsg  string
		wantClas string
	}{
		// MessageOutputLengthError: the ONE shipping variant whose data carries no
		// message (data: {}). ErrorMessage stays empty; ErrorClass is the name.
		{"message_less_variant", "MessageOutputLengthError", map[string]any{}, "", "MessageOutputLengthError"},
		// data present but message is not a string (a non-object/garbage body that
		// still unmarshals into the {message string} probe as zero) → empty.
		{"data_without_message", "UnknownError", map[string]any{"ref": "r1"}, "", "UnknownError"},
		// error with only a name, no data key at all → empty message.
		{"no_data_key", "ProviderError", nil, "", "ProviderError"},
		// empty-name error object: error PRESENCE still makes it failed (P2-A), the
		// class defaults; the data.message still flows through.
		{"empty_name_keeps_message", "", map[string]any{"message": "boom"}, "boom", defaultErrorClass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := rootSession("ses_err", 0)
			msgs := []messageWithParts{mwp(asgMsgErrData(t, "msg_a", tc.errName, tc.errData))}
			evs := run(t, s, msgs)
			fins := finalizes(evs)
			if len(fins) != 1 {
				t.Fatalf("SessionFinalized count = %d, want 1", len(fins))
			}
			if fins[0].Status != canonical.StatusFailed {
				t.Fatalf("Status = %q, want failed", fins[0].Status)
			}
			if fins[0].ErrorClass != tc.wantClas {
				t.Errorf("ErrorClass = %q, want %q", fins[0].ErrorClass, tc.wantClas)
			}
			if fins[0].ErrorMessage != tc.wantMsg {
				t.Errorf("ErrorMessage = %q, want %q", fins[0].ErrorMessage, tc.wantMsg)
			}
		})
	}
}

// TestP3_1_ErrorMessageHelper pins the pure helper's branches directly, including
// the malformed-JSON path that the mapSession-level tests cannot reach (the
// assistant decode would reject a malformed whole-message body before errorMessage
// is consulted, but a corrupt error.data sub-blob is possible).
func TestP3_1_ErrorMessageHelper(t *testing.T) {
	cases := []struct {
		name string
		err  *assistantError
		want string
	}{
		{"message_present", &assistantError{Name: "X", Data: json.RawMessage(`{"message":"hello"}`)}, "hello"},
		{"nil_data", &assistantError{Name: "X", Data: nil}, ""},
		{"empty_data", &assistantError{Name: "X", Data: json.RawMessage(``)}, ""},
		{"whitespace_data", &assistantError{Name: "X", Data: json.RawMessage("  \n ")}, ""},
		{"null_data", &assistantError{Name: "X", Data: json.RawMessage(`null`)}, ""},
		{"no_message_key", &assistantError{Name: "X", Data: json.RawMessage(`{"ref":"r"}`)}, ""},
		{"malformed_data", &assistantError{Name: "X", Data: json.RawMessage(`{not json`)}, ""},
		{"non_object_data", &assistantError{Name: "X", Data: json.RawMessage(`"a string"`)}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorMessage(tc.err); got != tc.want {
				t.Errorf("errorMessage = %q, want %q", got, tc.want)
			}
		})
	}
}
