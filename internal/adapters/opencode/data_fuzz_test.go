package opencode

import "testing"

// This file fuzzes the opencode `data`-JSON decoders — decodeMessageData (the
// message.data user|assistant union) and decodePartData (the part.data 12-variant
// $.type union) in types.go. They are opencode's analogue of codex's JSONL line
// parser (codex/parser_fuzz_test.go FuzzParseLine): the untrusted-bytes boundary
// where a malformed/truncated/adversarial blob from a live, concurrently-written
// SQLite database first meets the adapter. The contract under fuzz is the same:
// the decoder NEVER panics on any input — it returns either a decoded struct or a
// wrapped error. Every typed helper reachable from a SUCCESSFULLY decoded value
// (role/kind/subAgentSessionID/modelID/reasoningKind) must also not panic. All
// seeds are synthetic with placeholder identities (no real session content).

// messageDataSeeds covers both message roles plus the malformed/edge inputs.
func messageDataSeeds() [][]byte {
	return [][]byte{
		// Valid user message (nested model object, summary, tools).
		[]byte(`{"id":"msg_u1","sessionID":"ses_x","role":"user","time":{"created":1000},"agent":"general","model":{"providerID":"anthropic","modelID":"claude-x","variant":"default"},"tools":{"read":true}}`),
		// Valid assistant message with tokens/cost/finish.
		[]byte(`{"id":"msg_a1","sessionID":"ses_x","role":"assistant","parentID":"msg_u1","agent":"general","modelID":"claude-x","providerID":"anthropic","mode":"general","cost":0.02,"tokens":{"total":1000,"input":500,"output":80,"reasoning":16,"cache":{"read":100,"write":0}},"time":{"created":2000,"completed":9000},"finish":"stop"}`),
		// Assistant with an error (tagged AssistantError union).
		[]byte(`{"role":"assistant","modelID":"claude-x","providerID":"anthropic","error":{"name":"ProviderError","data":{"message":"boom"}},"time":{"created":2000}}`),
		// Assistant with completed absent (still-running turn).
		[]byte(`{"role":"assistant","modelID":"m","providerID":"p","time":{"created":2000}}`),
		// Unknown role (forward-compat → roleUnknown, not an error).
		[]byte(`{"role":"system","time":{"created":1}}`),
		// Role absent entirely.
		[]byte(`{"time":{"created":1}}`),
		// Numbers as strings / wrong types in a sibling field (dropped/ignored).
		[]byte(`{"role":"assistant","cost":"not-a-number"}`),
		// Empty object / null / blank / whitespace / garbage.
		[]byte(`{}`),
		[]byte(`null`),
		[]byte(``),
		[]byte(`   `),
		[]byte(`{not json`),
		// Deeply-nested blob (defends against unbounded recursion / stack issues).
		[]byte(`{"role":"assistant","tokens":{"cache":{"read":1}},"error":{"data":{"a":{"b":{"c":{"d":{"e":[1,2,3]}}}}}}}`),
	}
}

// partDataSeeds covers each of the 12 known $.type variants + an unknown type +
// the malformed/edge inputs.
func partDataSeeds() [][]byte {
	return [][]byte{
		[]byte(`{"type":"step-start","snapshot":"snap_1"}`),
		[]byte(`{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":100,"output":20,"reasoning":0,"cache":{"read":0,"write":0}}}`),
		[]byte(`{"type":"text","text":"hello","synthetic":false,"time":{"start":1,"end":2}}`),
		[]byte(`{"type":"reasoning","text":"thinking","time":{"start":1,"end":2},"metadata":{"summary":true}}`),
		// tool with state.metadata.sessionId (the sub-agent task edge, AC#4).
		[]byte(`{"type":"tool","callID":"call_1","tool":"task","state":{"status":"completed","input":{"prompt":"go"},"output":"done","metadata":{"sessionId":"ses_child"},"time":{"start":1,"end":2}}}`),
		// tool error state.
		[]byte(`{"type":"tool","callID":"c","tool":"bash","state":{"status":"error","input":{"cmd":"x"},"error":"exit 1","time":{"start":1,"end":2}}}`),
		// tool running (no end → still running).
		[]byte(`{"type":"tool","callID":"c","tool":"read","state":{"status":"running","input":{},"time":{"start":1}}}`),
		// tool with a null/absent state.
		[]byte(`{"type":"tool","callID":"c","tool":"grep","state":null}`),
		[]byte(`{"type":"patch","hash":"abc123","files":["/work/a.go","/work/b.go"]}`),
		[]byte(`{"type":"snapshot","snapshot":"hash_1"}`),
		[]byte(`{"type":"compaction","auto":true,"overflow":false}`),
		[]byte(`{"type":"retry","attempt":2,"error":{"name":"APIError"},"time":{"created":1}}`),
		[]byte(`{"type":"file","mime":"image/png","filename":"x.png","url":"https://example.invalid/x.png"}`),
		[]byte(`{"type":"subtask","prompt":"do x","description":"d","agent":"reviewer"}`),
		[]byte(`{"type":"agent","name":"reviewer","source":"task"}`),
		// metadata.sessionId present but state absent (subAgentSessionID must be safe).
		[]byte(`{"type":"tool","tool":"task","callID":"c"}`),
		// metadata that is not an object (subAgentSessionID guards malformed).
		[]byte(`{"type":"tool","tool":"task","state":{"status":"completed","metadata":"oops","time":{"start":1,"end":2}}}`),
		// Unknown $.type (forward-compat → partUnknown, not an error).
		[]byte(`{"type":"brand_new_part","x":1}`),
		// $.type absent.
		[]byte(`{"text":"orphan"}`),
		// Empty / null / blank / garbage.
		[]byte(`{}`),
		[]byte(`null`),
		[]byte(``),
		[]byte(`  `),
		[]byte(`{"type":`),
		// Deeply-nested state blob.
		[]byte(`{"type":"tool","tool":"x","state":{"status":"completed","input":{"a":{"b":{"c":{"d":{"e":1}}}}},"metadata":{"sessionId":"s"},"time":{"start":1,"end":2}}}`),
	}
}

// FuzzDecodeMessageData feeds arbitrary bytes into decodeMessageData. Contract:
// never panics; returns a struct or a wrapped error. On a decoded value, the
// reachable typed helpers (role, modelID via the nested model object) must also
// not panic.
func FuzzDecodeMessageData(f *testing.F) {
	for _, s := range messageDataSeeds() {
		f.Add(s)
	}
	// Cross-seed: a part body fed to the message decoder must also be safe.
	for _, s := range partDataSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		d, err := decodeMessageData(data)
		if err == nil {
			_ = d.role()
			if d.Model != nil {
				_ = d.Model.modelID()
			}
		}
	})
}

// FuzzDecodePartData feeds arbitrary bytes into decodePartData. Contract: never
// panics; returns a struct or a wrapped error. On a decoded value, kind() and the
// tool-state sub-agent extraction (subAgentSessionID, which parses raw metadata)
// must also not panic.
func FuzzDecodePartData(f *testing.F) {
	for _, s := range partDataSeeds() {
		f.Add(s)
	}
	// Cross-seed: a message body fed to the part decoder must also be safe.
	for _, s := range messageDataSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		d, err := decodePartData(data)
		if err == nil {
			_ = d.kind()
			_ = reasoningKind(data)
			if d.State != nil {
				_ = d.State.subAgentSessionID()
			}
		}
	})
}
