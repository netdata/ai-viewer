// Command genfixtures regenerates the synthetic .json.gz fixtures used
// by the ai-agent v2 adapter's golden tests. Operator-runnable; CI
// never invokes it directly. See scripts/genfixtures-v2.sh.
//
// Run from the repository root:
//
//	go run ./internal/adapters/aiagent_v2/cmd/genfixtures
//
// All fixtures land under testdata/aiagent_v2/<scenario>/INPUT/.
// Deterministic: the same source produces byte-identical output.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// outputRoot is the testdata destination. The generator writes only
// here; never touches operator data.
const outputRoot = "testdata/aiagent_v2"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "genfixtures: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	scenarios := []struct {
		dir       string
		originID  string
		build     func(originID string) map[string]any
		writeRaw  []byte // when non-nil, write verbatim instead of building+gzipping
		filename  string // override the filename (default: <originID>.json.gz)
		emptyFile bool
		skipFile  bool // when true, do not emit a snapshot file (directory only)
	}{
		{
			dir:      "happy_v2_single_turn",
			originID: "00000000-0000-4000-8000-000000000001",
			build:    happyV2,
		},
		{
			dir:      "happy_v1_legacy",
			originID: "00000000-0000-4000-8000-000000000002",
			build:    happyV1,
		},
		{
			dir:      "embedded_sub_agent",
			originID: "00000000-0000-4000-8000-000000000003",
			build:    embeddedSubAgent,
		},
		{
			dir:      "multi_descendant_same_file",
			originID: "00000000-0000-4000-8000-000000000004",
			build:    multiDescendantSameFile,
		},
		{
			dir:      "init_turn_zero",
			originID: "00000000-0000-4000-8000-000000000005",
			build:    initTurnZero,
		},
		{
			dir:      "system_op_kind",
			originID: "00000000-0000-4000-8000-000000000006",
			build:    systemOpKind,
		},
		{
			dir:      "tool_chars_accounting",
			originID: "00000000-0000-4000-8000-000000000007",
			build:    toolCharsAccounting,
		},
		{
			dir:      "final_report",
			originID: "00000000-0000-4000-8000-000000000008",
			build:    finalReport,
		},
		{
			dir:       "zero_byte",
			originID:  "00000000-0000-4000-8000-000000000009",
			emptyFile: true,
		},
		{
			dir:      "tmp_file",
			originID: "00000000-0000-4000-8000-00000000000a",
			filename: "00000000-0000-4000-8000-00000000000a.json.gz.tmp-1234-5678",
			writeRaw: []byte("orphan tmp; adapter must ignore"),
		},
	}

	for _, s := range scenarios {
		inputDir := filepath.Join(outputRoot, s.dir, "INPUT")
		if err := os.MkdirAll(inputDir, 0o750); err != nil {
			return fmt.Errorf("mkdir %s: %w", inputDir, err)
		}

		filename := s.filename
		if filename == "" {
			filename = s.originID + ".json.gz"
		}
		dest := filepath.Join(inputDir, filename)

		if s.skipFile {
			continue
		}
		if s.emptyFile {
			if err := os.WriteFile(dest, []byte{}, 0o600); err != nil {
				return fmt.Errorf("write empty %s: %w", dest, err)
			}
			fmt.Printf("wrote (empty) %s\n", dest)
			continue
		}
		if s.writeRaw != nil {
			if err := os.WriteFile(dest, s.writeRaw, 0o600); err != nil {
				return fmt.Errorf("write raw %s: %w", dest, err)
			}
			// Also emit a valid snapshot in the same dir so the test can
			// distinguish "tmp file is ignored" from "directory is empty".
			validName := s.originID + ".json.gz"
			validDest := filepath.Join(inputDir, validName)
			body, err := buildJSON(map[string]any{
				"version": 2,
				"reason":  "final",
				"opTree": map[string]any{
					"id":        "tmp-test",
					"traceId":   s.originID,
					"agentId":   "tmp-test-agent",
					"startedAt": int64(1700000000000),
					"endedAt":   int64(1700000001000),
					"success":   true,
					"turns":     []any{},
					"steps":     []any{},
				},
			})
			if err != nil {
				return err
			}
			gz, err := gzipBytes(body)
			if err != nil {
				return err
			}
			if err := os.WriteFile(validDest, gz, 0o600); err != nil {
				return fmt.Errorf("write valid %s: %w", validDest, err)
			}
			fmt.Printf("wrote %s (tmp orphan + valid snapshot)\n", dest)
			continue
		}

		body, err := buildJSON(s.build(s.originID))
		if err != nil {
			return fmt.Errorf("build %s: %w", s.dir, err)
		}
		gz, err := gzipBytes(body)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dest, gz, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		fmt.Printf("wrote %s (%d bytes uncompressed → %d gz)\n", dest, len(body), len(gz))
	}
	return nil
}

// buildJSON marshals v deterministically with sorted map keys.
func buildJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// gzipBytes wraps the standard library gzip writer. Strips name + mtime
// header bytes via NewWriter (default) so two runs produce byte-
// identical output.
func gzipBytes(body []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------- fixture builders ----------

func happyV2(originID string) map[string]any {
	return map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":           "happy-v2",
			"traceId":      originID,
			"agentId":      "test-happy",
			"callPath":     "test-happy",
			"sessionTitle": "happy v2 fixture",
			"startedAt":    int64(1700000000000),
			"endedAt":      int64(1700000010000),
			"success":      true,
			"turns": []any{
				turnLLM(1, 1700000001000, 1700000005000),
			},
			"steps": []any{},
		},
	}
}

func happyV1(originID string) map[string]any {
	return map[string]any{
		"version": 1,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "happy-v1",
			"traceId":   originID,
			"agentId":   "test-legacy",
			"startedAt": int64(1700000000000),
			"endedAt":   int64(1700000003000),
			"success":   true,
			"turns": []any{
				turnLLM(1, 1700000000500, 1700000002500),
			},
		},
	}
}

func embeddedSubAgent(originID string) map[string]any {
	parent := happyV2(originID)
	parentTree := parent["opTree"].(map[string]any)
	parentTree["sessionTitle"] = "parent with sub-agent"
	parentTree["turns"].([]any)[0].(map[string]any)["ops"] = []any{
		opLLM("op-llm-1", 1700000001500, 1700000002500),
		map[string]any{
			"opId":      "op-sub-1",
			"kind":      "session",
			"startedAt": int64(1700000002500),
			"endedAt":   int64(1700000004500),
			"status":    "ok",
			"attributes": map[string]any{
				"name":     "research-sub-agent",
				"provider": "subagent",
				"kind":     "agent",
				"size":     1500,
			},
			"logs":       []any{},
			"accounting": []any{},
			"childSession": map[string]any{
				"id":        "child-tree",
				"traceId":   "11111111-1111-4111-8111-111111111111",
				"agentId":   "research-sub-agent",
				"callPath":  "test-happy->research-sub-agent",
				"startedAt": int64(1700000002600),
				"endedAt":   int64(1700000004400),
				"success":   true,
				"turns": []any{
					turnLLM(1, 1700000002700, 1700000004300),
				},
				"steps": []any{},
			},
		},
	}
	return parent
}

func multiDescendantSameFile(originID string) map[string]any {
	// Parent + 2 embedded children at different points in the opTree.
	parent := happyV2(originID)
	parentTree := parent["opTree"].(map[string]any)
	parentTree["sessionTitle"] = "parent with 2 children"
	parentTree["turns"].([]any)[0].(map[string]any)["ops"] = []any{
		map[string]any{
			"opId":      "op-sub-a",
			"kind":      "session",
			"startedAt": int64(1700000001500),
			"endedAt":   int64(1700000003500),
			"status":    "ok",
			"attributes": map[string]any{
				"name":     "child-a",
				"provider": "subagent",
				"kind":     "agent",
			},
			"logs":       []any{},
			"accounting": []any{},
			"childSession": map[string]any{
				"id":        "child-a-tree",
				"traceId":   "22222222-aaaa-4222-8222-aaaaaaaaaaaa",
				"agentId":   "child-a",
				"startedAt": int64(1700000001600),
				"endedAt":   int64(1700000003400),
				"success":   true,
				"turns":     []any{turnLLM(1, 1700000001700, 1700000003300)},
				"steps":     []any{},
			},
		},
		map[string]any{
			"opId":      "op-sub-b",
			"kind":      "session",
			"startedAt": int64(1700000004000),
			"endedAt":   int64(1700000005000),
			"status":    "ok",
			"attributes": map[string]any{
				"name":     "child-b",
				"provider": "subagent",
				"kind":     "agent",
			},
			"logs":       []any{},
			"accounting": []any{},
			"childSession": map[string]any{
				"id":        "child-b-tree",
				"traceId":   "22222222-bbbb-4222-8222-bbbbbbbbbbbb",
				"agentId":   "child-b",
				"startedAt": int64(1700000004100),
				"endedAt":   int64(1700000004900),
				"success":   true,
				"turns":     []any{turnLLM(1, 1700000004200, 1700000004800)},
				"steps":     []any{},
			},
		},
	}
	return parent
}

func initTurnZero(originID string) map[string]any {
	return map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "init-test",
			"traceId":   originID,
			"agentId":   "init-test",
			"startedAt": int64(1700000000000),
			"endedAt":   int64(1700000010000),
			"success":   true,
			"turns": []any{
				map[string]any{
					"id":         "turn-0",
					"index":      0,
					"startedAt":  int64(1700000000000),
					"endedAt":    int64(1700000000500),
					"attributes": map[string]any{"system": true, "label": "init"},
					"ops": []any{
						map[string]any{
							"opId":       "init-op",
							"kind":       "system",
							"startedAt":  int64(1700000000050),
							"endedAt":    int64(1700000000450),
							"status":     "ok",
							"attributes": map[string]any{"label": "init"},
							"logs":       []any{},
							"accounting": []any{},
						},
					},
				},
				turnLLM(1, 1700000001000, 1700000009000),
			},
			"steps": []any{},
		},
	}
}

func systemOpKind(originID string) map[string]any {
	return map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "sys-test",
			"traceId":   originID,
			"agentId":   "sys-test",
			"startedAt": int64(1700000000000),
			"endedAt":   int64(1700000002000),
			"success":   true,
			"turns": []any{
				map[string]any{
					"id":        "turn-1",
					"index":     1,
					"startedAt": int64(1700000000500),
					"endedAt":   int64(1700000001500),
					"ops": []any{
						map[string]any{
							"opId":       "sys-op-1",
							"kind":       "system",
							"startedAt":  int64(1700000000600),
							"endedAt":    int64(1700000000700),
							"status":     "ok",
							"attributes": map[string]any{"label": "tick"},
							"logs":       []any{},
							"accounting": []any{},
						},
						map[string]any{
							"opId":       "sys-op-2",
							"kind":       "system",
							"startedAt":  int64(1700000001100),
							"endedAt":    int64(1700000001200),
							"status":     "ok",
							"attributes": map[string]any{"label": "drain"},
							"logs":       []any{},
							"accounting": []any{},
						},
					},
				},
			},
			"steps": []any{},
		},
	}
}

func toolCharsAccounting(originID string) map[string]any {
	return map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "tool-test",
			"traceId":   originID,
			"agentId":   "tool-test",
			"startedAt": int64(1700000000000),
			"endedAt":   int64(1700000005000),
			"success":   true,
			"turns": []any{
				map[string]any{
					"id":        "turn-1",
					"index":     1,
					"startedAt": int64(1700000001000),
					"endedAt":   int64(1700000004000),
					"ops": []any{
						map[string]any{
							"opId":      "tool-op-1",
							"kind":      "tool",
							"startedAt": int64(1700000001500),
							"endedAt":   int64(1700000003500),
							"status":    "ok",
							"attributes": map[string]any{
								"name":     "shell",
								"provider": "builtin",
								"kind":     "command",
							},
							"logs": []any{},
							"accounting": []any{
								map[string]any{
									"type":          "tool",
									"charactersIn":  120,
									"charactersOut": 4500,
									"latency":       2000,
									"status":        "ok",
									"command":       "<redacted>",
								},
							},
						},
					},
				},
			},
			"steps": []any{},
		},
	}
}

func finalReport(originID string) map[string]any {
	return map[string]any{
		"version": 2,
		"reason":  "final",
		"opTree": map[string]any{
			"id":        "final-report-test",
			"traceId":   originID,
			"agentId":   "report-agent",
			"startedAt": int64(1700000000000),
			"endedAt":   int64(1700000010000),
			"success":   true,
			"finalReport": map[string]any{
				"format":   "json",
				"captured": true,
				"summary":  "All checks passed.",
			},
			"pluginMetas": map[string]any{
				"linter":   map[string]any{"warnings": 0, "errors": 0},
				"security": map[string]any{"high": 0, "medium": 0},
			},
			"turns": []any{turnLLM(1, 1700000001000, 1700000009000)},
			"steps": []any{},
		},
	}
}

// ---------- shared helpers ----------

func turnLLM(index int, started, ended int64) map[string]any {
	return map[string]any{
		"id":        fmt.Sprintf("turn-%d", index),
		"index":     index,
		"startedAt": started,
		"endedAt":   ended,
		"ops": []any{
			opLLM(fmt.Sprintf("op-llm-t%d", index), started+100, ended-100),
		},
	}
}

func opLLM(opID string, started, ended int64) map[string]any {
	return map[string]any{
		"opId":      opID,
		"kind":      "llm",
		"startedAt": started,
		"endedAt":   ended,
		"status":    "ok",
		"attributes": map[string]any{
			"provider": "anthropic",
			"model":    "claude-3-5-sonnet",
			"name":     "claude-3-5-sonnet",
		},
		"logs": []any{},
		"accounting": []any{
			map[string]any{
				"type":     "llm",
				"provider": "anthropic",
				"model":    "claude-3-5-sonnet",
				"costUsd":  0.012345,
				"tokens": map[string]any{
					"inputTokens":          100,
					"outputTokens":         50,
					"cacheReadInputTokens": 10,
				},
				"latency":    int(ended - started),
				"status":     "ok",
				"stopReason": "end_turn",
			},
		},
	}
}
