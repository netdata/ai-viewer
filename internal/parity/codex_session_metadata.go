package parity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
)

const codexFormat = "codex"

type codexSessionMetadataIdentity struct {
	NativeSessionID string `json:"native_session_id"`
	AgentName       string `json:"agent_name,omitempty"`
	CwdSHA256       string `json:"cwd_sha256,omitempty"`
	CLIVersion      string `json:"cli_version,omitempty"`
	Originator      string `json:"originator,omitempty"`
	Source          string `json:"source,omitempty"`
	ModelProvider   string `json:"model_provider,omitempty"`
	GitSHA256       string `json:"git_sha256,omitempty"`
	Relationship    string `json:"relationship,omitempty"`
	SubagentDepth   int64  `json:"subagent_depth,omitempty"`
}

type codexSessionMetaPayload struct {
	ID             string               `json:"id"`
	ForkedFromID   string               `json:"forked_from_id"`
	ParentThreadID string               `json:"parent_thread_id"`
	Cwd            string               `json:"cwd"`
	Originator     string               `json:"originator"`
	CLIVersion     string               `json:"cli_version"`
	ThreadSource   string               `json:"thread_source"`
	AgentNickname  string               `json:"agent_nickname"`
	AgentRole      string               `json:"agent_role"`
	ModelProvider  string               `json:"model_provider"`
	Source         json.RawMessage      `json:"source"`
	Git            *codexSessionGitInfo `json:"git"`
}

type codexSessionGitInfo struct {
	CommitHash    string `json:"commit_hash"`
	Branch        string `json:"branch"`
	RepositoryURL string `json:"repository_url"`
}

type codexSessionSourceKind string

const (
	codexSessionSourceRoot     codexSessionSourceKind = "root"
	codexSessionSourceSubagent codexSessionSourceKind = "sub_agent"
	codexSessionSourceInternal codexSessionSourceKind = "tool_internal"
	codexSessionSourceOther    codexSessionSourceKind = "other"
)

func codexSessionMetadataIdentityFromSource(nativeSessionID string, body codexSessionMetaPayload) (codexSessionMetadataIdentity, bool, error) {
	if body.ID != "" {
		nativeSessionID = body.ID
	}
	identity := codexSessionMetadataIdentity{
		NativeSessionID: nativeSessionID,
		AgentName:       codexAgentNameFromMeta(body),
		CLIVersion:      body.CLIVersion,
		Originator:      body.Originator,
		Source:          codexSourceString(body.Source),
		ModelProvider:   body.ModelProvider,
		Relationship:    codexSessionRelationship(body),
		SubagentDepth:   int64(codexSubagentDepth(body.Source)),
	}
	if body.Cwd != "" {
		identity.CwdSHA256 = stringSHA256(body.Cwd)
	}
	if body.Git != nil {
		hash, err := codexGitHash(body.Git)
		if err != nil {
			return codexSessionMetadataIdentity{}, false, err
		}
		identity.GitSHA256 = hash
	}
	if !identity.hasDescriptiveCodexMetadata() {
		return codexSessionMetadataIdentity{}, false, nil
	}
	return identity, true, nil
}

func (i codexSessionMetadataIdentity) hasDescriptiveCodexMetadata() bool {
	return i.AgentName != "" && i.AgentName != "codex" ||
		i.CwdSHA256 != "" ||
		i.CLIVersion != "" ||
		i.Originator != "" ||
		i.Source != "" ||
		i.ModelProvider != "" ||
		i.GitSHA256 != "" ||
		i.Relationship != "" ||
		i.SubagentDepth != 0
}

func codexAgentNameFromMeta(p codexSessionMetaPayload) string {
	if p.AgentNickname != "" {
		return p.AgentNickname
	}
	if p.AgentRole != "" {
		return p.AgentRole
	}
	base := "codex"
	if p.Originator != "" {
		base = "codex:" + p.Originator
	}
	if p.Cwd != "" {
		kind := codexClassifySource(p.Source)
		if kind != codexSessionSourceSubagent {
			if cwdBase := filepath.Base(p.Cwd); cwdBase != "" && cwdBase != "." && cwdBase != "/" {
				return base + " (" + cwdBase + ")"
			}
		}
	}
	return base
}

func codexSessionRelationship(p codexSessionMetaPayload) string {
	kind := codexClassifySource(p.Source)
	switch {
	case kind == codexSessionSourceSubagent:
		return "sub_agent"
	case p.ForkedFromID != "":
		return "fork"
	case kind == codexSessionSourceInternal:
		return "tool_internal"
	case p.ThreadSource == "subagent":
		return "sub_agent"
	case p.ThreadSource == "memory_consolidation":
		return "tool_internal"
	default:
		return ""
	}
}

func codexSourceSessionKindAndParent(p codexSessionMetaPayload) (string, string) {
	kind := codexClassifySource(p.Source)
	switch {
	case kind == codexSessionSourceSubagent:
		parent := codexParentThreadIDFromSource(p.Source)
		if parent == "" {
			parent = p.ParentThreadID
		}
		return "sub_agent", parent
	case p.ForkedFromID != "":
		return "fork", p.ForkedFromID
	case kind == codexSessionSourceInternal:
		return "tool_internal", ""
	case p.ThreadSource == "subagent":
		return "sub_agent", p.ParentThreadID
	case p.ThreadSource == "memory_consolidation":
		return "tool_internal", ""
	default:
		return "root", ""
	}
}

func codexClassifySource(raw json.RawMessage) codexSessionSourceKind {
	body := bytes.TrimSpace(raw)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return codexSessionSourceRoot
	}
	var s string
	if json.Unmarshal(body, &s) == nil {
		switch s {
		case "cli", "vscode", "exec", "mcp", "unknown":
			return codexSessionSourceRoot
		default:
			return codexSessionSourceOther
		}
	}
	var obj struct {
		Custom   json.RawMessage `json:"custom"`
		Internal json.RawMessage `json:"internal"`
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(body, &obj) != nil {
		return codexSessionSourceOther
	}
	switch {
	case len(obj.Custom) > 0:
		return codexSessionSourceRoot
	case len(obj.Internal) > 0:
		return codexSessionSourceInternal
	case len(obj.Subagent) > 0:
		return codexSessionSourceSubagent
	default:
		return codexSessionSourceOther
	}
}

func codexSourceString(raw json.RawMessage) string {
	body := bytes.TrimSpace(raw)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(body, &s) == nil {
		return s
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return ""
	}
	for _, key := range []string{"subagent", "internal", "custom", "other"} {
		if _, ok := obj[key]; ok {
			return key
		}
	}
	return ""
}

func codexParentThreadIDFromSource(raw json.RawMessage) string {
	body := bytes.TrimSpace(raw)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return ""
	}
	var obj struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(body, &obj) != nil || len(obj.Subagent) == 0 {
		return ""
	}
	var nested struct {
		ThreadSpawn struct {
			ParentThreadID string `json:"parent_thread_id"`
		} `json:"thread_spawn"`
	}
	if json.Unmarshal(obj.Subagent, &nested) != nil {
		return ""
	}
	return nested.ThreadSpawn.ParentThreadID
}

func codexSubagentDepth(raw json.RawMessage) int {
	body := bytes.TrimSpace(raw)
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return 0
	}
	var obj struct {
		Subagent struct {
			ThreadSpawn struct {
				Depth int `json:"depth"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal(body, &obj) != nil {
		return 0
	}
	return obj.Subagent.ThreadSpawn.Depth
}

func codexGitHash(git *codexSessionGitInfo) (string, error) {
	fields := map[string]any{}
	if git.CommitHash != "" {
		fields["commit_hash"] = git.CommitHash
	}
	if git.Branch != "" {
		fields["branch"] = git.Branch
	}
	if git.RepositoryURL != "" {
		fields["repository_url"] = git.RepositoryURL
	}
	if len(fields) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return canonicalJSONHash(raw)
}

func artifactFromCodexSessionMetadata(row canonicalSessionRow) (Artifact, bool, error) {
	identity, ok, err := codexSessionMetadataFromCanonical(row)
	if err != nil || !ok {
		return Artifact{}, ok, err
	}
	artifact, err := identityArtifact(identityArtifactInput{
		sourceID:           row.sourceID,
		adapter:            row.adapter,
		sourceFile:         row.sourceLocation,
		canonicalSessionID: row.sessionID,
		nativeSessionID:    row.nativeSessionID,
		nativeArtifactID:   "session:" + row.nativeSessionID + ":metadata",
		class:              ClassSessionMetadata,
		selectorURI:        "canonical://sessions/" + url.PathEscape(row.sessionID) + "#metadata",
		identity:           identity,
	})
	return artifact, true, err
}

func codexSessionMetadataFromCanonical(row canonicalSessionRow) (codexSessionMetadataIdentity, bool, error) {
	fields, err := jsonObjectFromNullString(row.extrasJSON, "codex session extras")
	if err != nil {
		return codexSessionMetadataIdentity{}, false, err
	}
	identity := codexSessionMetadataIdentity{
		NativeSessionID: row.nativeSessionID,
		AgentName:       nullString(row.agentName),
	}
	cwd, err := stringJSONField(fields, "cwd")
	if err != nil {
		return codexSessionMetadataIdentity{}, false, err
	}
	if cwd != "" {
		identity.CwdSHA256 = stringSHA256(cwd)
	}
	if identity.CLIVersion, err = stringJSONField(fields, "cli_version"); err != nil {
		return codexSessionMetadataIdentity{}, false, err
	}
	if identity.Originator, err = stringJSONField(fields, "originator"); err != nil {
		return codexSessionMetadataIdentity{}, false, err
	}
	if identity.Source, err = stringJSONField(fields, "source"); err != nil {
		return codexSessionMetadataIdentity{}, false, err
	}
	if identity.ModelProvider, err = stringJSONField(fields, "model_provider"); err != nil {
		return codexSessionMetadataIdentity{}, false, err
	}
	if identity.Relationship, err = stringJSONField(fields, "relationship"); err != nil {
		return codexSessionMetadataIdentity{}, false, err
	}
	if identity.SubagentDepth, err = int64JSONField(fields, "subagent_depth"); err != nil {
		return codexSessionMetadataIdentity{}, false, err
	}
	if raw, ok := fields["git"]; ok && !jsonRawEmptyObjectOrNull(raw) {
		identity.GitSHA256, err = canonicalJSONHash(raw)
		if err != nil {
			return codexSessionMetadataIdentity{}, false, fmt.Errorf("hash codex git metadata: %w", err)
		}
	}
	if !identity.hasDescriptiveCodexMetadata() {
		return codexSessionMetadataIdentity{}, false, nil
	}
	return identity, true, nil
}
