// Package main validates the DB/API/TypeScript/UI field-contract matrix.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

type row struct {
	line   int
	values map[string]string
}

type stringSet map[string]struct{}

func main() {
	repoRoot := flag.String("repo", ".", "repository root")
	matrixPath := flag.String("matrix", "testdata/contracts/field-matrix.yaml", "field matrix path, relative to repo root unless absolute")
	flag.Parse()

	root, err := filepath.Abs(*repoRoot)
	if err != nil {
		fail([]string{fmt.Sprintf("contract-matrix: resolve repo root: %v", err)})
	}
	matrix := *matrixPath
	if !filepath.IsAbs(matrix) {
		matrix = filepath.Join(root, matrix)
	}

	rows, err := parseRows(matrix)
	if err != nil {
		fail([]string{fmt.Sprintf("contract-matrix: %v", err)})
	}

	goFields, err := collectGoJSONFields(filepath.Join(root, "internal/presenter"))
	if err != nil {
		fail([]string{fmt.Sprintf("contract-matrix: collect Go presenter fields: %v", err)})
	}
	tsFields, err := collectTSFields(
		filepath.Join(root, "frontend/src/api/types.ts"),
		filepath.Join(root, "frontend/src/api/payloads.ts"),
		filepath.Join(root, "frontend/src/viz/trace.ts"),
	)
	if err != nil {
		fail([]string{fmt.Sprintf("contract-matrix: collect TypeScript contract fields: %v", err)})
	}

	drift := validateRows(root, rows, goFields, tsFields)
	if len(drift) > 0 {
		fail(drift)
	}
	fmt.Printf("[PASS] contract-matrix: %d rows verified\n", len(rows))
}

func fail(lines []string) {
	fmt.Fprintln(os.Stderr, "[FAIL] contract-matrix drift:")
	for _, line := range lines {
		fmt.Fprintf(os.Stderr, "  - %s\n", line)
	}
	os.Exit(1)
}

func parseRows(path string) ([]row, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- SOW-0105 contract gate reads repo-controlled matrix paths resolved by the wrapper/self-test.
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var rows []row
	var current *row
	inRows := false
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == "rows:" {
			inRows = true
			continue
		}
		if !inRows {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			if current != nil {
				rows = append(rows, *current)
			}
			current = &row{line: lineNo, values: map[string]string{}}
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if rest != "" {
				key, value, err := parseKeyValue(rest)
				if err != nil {
					return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
				}
				current.values[key] = value
			}
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("%s:%d: row field before first row", path, lineNo)
		}
		key, value, err := parseKeyValue(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		current.values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if current != nil {
		rows = append(rows, *current)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s: no rows found", path)
	}
	return rows, nil
}

func parseKeyValue(line string) (string, string, error) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("expected key: value")
	}
	key := strings.TrimSpace(line[:idx])
	if key == "" {
		return "", "", fmt.Errorf("empty key")
	}
	value := strings.TrimSpace(line[idx+1:])
	if value == "" {
		return key, "", nil
	}
	if strings.HasPrefix(value, "\"") {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", "", fmt.Errorf("invalid quoted scalar for %s: %w", key, err)
		}
		return key, unquoted, nil
	}
	if strings.Contains(value, "#") {
		value = strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
	}
	return key, value, nil
}

func validateRows(repoRoot string, rows []row, goFields, tsFields map[string]stringSet) []string {
	var drift []string
	required := []string{
		"entity", "field", "entity_kind", "db_column", "derived_from",
		"rest_surfaces", "typescript_types", "ui_surfaces", "state", "intent",
		"include_token", "privacy_class", "adapter_population", "index_status",
		"stats_dimension_eligible", "subscription_filter_eligible",
		"internal_reason", "sow_ref", "pending_ref", "test_refs", "artifact_class",
	}
	requiredSet := toSet(required)
	seen := map[string]int{}

	for _, r := range rows {
		prefix := fmt.Sprintf("line %d", r.line)
		for key := range r.values {
			if _, ok := requiredSet[key]; !ok {
				drift = append(drift, fmt.Sprintf("%s: unknown field %q", prefix, key))
			}
		}
		for _, key := range required {
			if _, ok := r.values[key]; !ok {
				drift = append(drift, fmt.Sprintf("%s: missing required field %q", prefix, key))
			}
		}
		if hasMissingKeys(r, required) {
			continue
		}

		checkEnum(&drift, prefix, r, "entity", allowedEntities())
		checkEnum(&drift, prefix, r, "entity_kind", allowedEntityKinds())
		checkEnum(&drift, prefix, r, "state", allowedStates())
		checkEnum(&drift, prefix, r, "intent", allowedIntents())
		checkEnum(&drift, prefix, r, "privacy_class", allowedPrivacyClasses())
		checkEnum(&drift, prefix, r, "adapter_population", allowedAdapterPopulation())
		checkEnum(&drift, prefix, r, "index_status", allowedIndexStatuses())
		checkEnum(&drift, prefix, r, "stats_dimension_eligible", allowedEligibility())
		checkEnum(&drift, prefix, r, "subscription_filter_eligible", allowedEligibility())

		if strings.TrimSpace(r.values["db_column"]) == "" && strings.TrimSpace(r.values["derived_from"]) == "" {
			drift = append(drift, fmt.Sprintf("%s: one of db_column or derived_from must be non-empty", prefix))
		}
		identity := strings.Join([]string{
			r.values["entity"],
			r.values["field"],
			r.values["rest_surfaces"],
			r.values["typescript_types"],
			r.values["include_token"],
		}, "\x00")
		if prev, ok := seen[identity]; ok {
			drift = append(drift, fmt.Sprintf("%s: duplicate row identity also defined at line %d", prefix, prev))
		} else {
			seen[identity] = r.line
		}

		validateList(&drift, prefix, r, "rest_surfaces", true)
		validateList(&drift, prefix, r, "typescript_types", true)
		validateList(&drift, prefix, r, "test_refs", false)
		validateInclude(&drift, prefix, r)
		validateInternalReason(&drift, prefix, r)
		validateArtifactClass(&drift, prefix, r)
		validateTestRefs(&drift, prefix, repoRoot, r)
		validateTypeNames(&drift, prefix, r, tsFields)
		validateEvidence(&drift, prefix, r, goFields, tsFields)
	}
	return drift
}

func hasMissingKeys(r row, keys []string) bool {
	for _, key := range keys {
		if _, ok := r.values[key]; !ok {
			return true
		}
	}
	return false
}

func checkEnum(drift *[]string, prefix string, r row, key string, allowed stringSet) {
	value := r.values[key]
	if _, ok := allowed[value]; !ok {
		*drift = append(*drift, fmt.Sprintf("%s: %s has invalid value %q", prefix, key, value))
	}
}

func validateList(drift *[]string, prefix string, r row, key string, required bool) {
	items := splitCSV(r.values[key])
	if required && len(items) == 0 {
		*drift = append(*drift, fmt.Sprintf("%s: %s must contain at least one value", prefix, key))
	}
	seen := map[string]struct{}{}
	for _, item := range items {
		if _, ok := seen[item]; ok {
			*drift = append(*drift, fmt.Sprintf("%s: %s duplicates %q", prefix, key, item))
		}
		seen[item] = struct{}{}
	}
}

func validateInclude(drift *[]string, prefix string, r row) {
	token := strings.TrimSpace(r.values["include_token"])
	if token != "" {
		if _, ok := allowedIncludeTokens()[token]; !ok {
			*drift = append(*drift, fmt.Sprintf("%s: include_token has invalid value %q", prefix, token))
		}
	}
	if r.values["state"] == "exposed-via-include" && token == "" {
		*drift = append(*drift, fmt.Sprintf("%s: exposed-via-include rows must name include_token", prefix))
	}
}

func validateInternalReason(drift *[]string, prefix string, r row) {
	state := r.values["state"]
	reason := strings.TrimSpace(r.values["internal_reason"])
	switch state {
	case "internal-only", "missing-default", "missing-completely":
		if reason == "" {
			*drift = append(*drift, fmt.Sprintf("%s: %s rows must explain internal_reason", prefix, state))
		}
	}
	if strings.TrimSpace(r.values["ui_surfaces"]) != "" && strings.TrimSpace(r.values["test_refs"]) == "" {
		*drift = append(*drift, fmt.Sprintf("%s: rows with ui_surfaces must name test_refs", prefix))
	}
}

func validateArtifactClass(drift *[]string, prefix string, r row) {
	class := strings.TrimSpace(r.values["artifact_class"])
	if r.values["entity"] != "payload_kind" {
		if class != "" {
			*drift = append(*drift, fmt.Sprintf("%s: artifact_class must be empty outside payload_kind rows", prefix))
		}
		return
	}
	expected := map[string]string{
		"llm_request":      "llm_request",
		"llm_response":     "llm_response",
		"llm_sdk_request":  "llm_sdk_request",
		"llm_sdk_response": "llm_sdk_response",
		"sdk_request":      "llm_sdk_request",
		"sdk_response":     "llm_sdk_response",
		"llm_reasoning":    "reasoning_text",
		"reasoning_stream": "reasoning_text",
		"tool_request":     "tool_request",
		"tool_response":    "tool_response",
		"log":              "log",
	}
	want, ok := expected[r.values["field"]]
	if !ok {
		*drift = append(*drift, fmt.Sprintf("%s: payload_kind field %q is not in the normalized payload-kind map", prefix, r.values["field"]))
		return
	}
	if class != want {
		*drift = append(*drift, fmt.Sprintf("%s: payload_kind %q artifact_class=%q, want %q", prefix, r.values["field"], class, want))
	}
}

func validateTestRefs(drift *[]string, prefix, repoRoot string, r row) {
	for _, ref := range splitCSV(r.values["test_refs"]) {
		if strings.HasPrefix(ref, "/") || strings.Contains(ref, "..") {
			*drift = append(*drift, fmt.Sprintf("%s: test_refs path must be repo-relative and contained: %s", prefix, ref))
			continue
		}
		switch {
		case strings.HasSuffix(ref, "_test.go"),
			strings.HasSuffix(ref, ".test.ts"),
			strings.HasSuffix(ref, ".test.tsx"),
			strings.HasSuffix(ref, "-test.sh"):
		default:
			*drift = append(*drift, fmt.Sprintf("%s: test_refs path has unsupported test filename: %s", prefix, ref))
		}
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(ref))); err != nil {
			*drift = append(*drift, fmt.Sprintf("%s: test_refs path does not exist: %s", prefix, ref))
		}
	}
}

func validateTypeNames(drift *[]string, prefix string, r row, tsFields map[string]stringSet) {
	for _, typ := range mappedTSTypes(splitCSV(r.values["typescript_types"])) {
		if _, ok := tsFields[typ]; !ok {
			*drift = append(*drift, fmt.Sprintf("%s: unknown TypeScript contract type %q", prefix, typ))
		}
	}
}

func validateEvidence(drift *[]string, prefix string, r row, goFields, tsFields map[string]stringSet) {
	if !directEvidenceApplies(r) {
		return
	}
	field := jsonFieldName(r.values["field"])
	if field == "" {
		return
	}
	goStructs := mappedGoStructs(splitCSV(r.values["typescript_types"]))
	tsTypes := mappedTSTypes(splitCSV(r.values["typescript_types"]))
	state := r.values["state"]

	switch state {
	case "exposed", "exposed-via-include":
		for _, st := range goStructs {
			if !hasField(goFields, st, field) {
				*drift = append(*drift, fmt.Sprintf("%s: %s row is %s but Go presenter struct %s lacks json field %q", prefix, r.values["field"], state, st, field))
			}
		}
		for _, typ := range tsTypes {
			if !hasField(tsFields, typ, field) {
				*drift = append(*drift, fmt.Sprintf("%s: %s row is %s but TypeScript interface %s lacks field %q", prefix, r.values["field"], state, typ, field))
			}
		}
	case "missing-default", "missing-completely", "internal-only":
		if len(goStructs) == 0 || len(tsTypes) == 0 {
			return
		}
		if allHaveField(goFields, goStructs, field) && allHaveField(tsFields, tsTypes, field) {
			*drift = append(*drift, fmt.Sprintf("%s: row is %s but Go and TypeScript already expose %q; update the matrix state", prefix, state, field))
		}
	}
}

func directEvidenceApplies(r row) bool {
	switch r.values["entity"] {
	case "session", "child_session", "turn", "op", "payload_ref", "source", "health", "log":
		return true
	default:
		return false
	}
}

func jsonFieldName(field string) string {
	switch field {
	case "model_provider_tokens_cost_context",
		"cwd_provider_alias_call_path_error_class":
		return ""
	}
	parts := strings.Split(field, ".")
	return strings.TrimSpace(parts[len(parts)-1])
}

func mappedGoStructs(types []string) []string {
	typeToStruct := map[string]string{
		"SessionListItem": "sessionListItem",
		"SessionDetail":   "sessionDetail",
		"ChildSummary":    "childSummary",
		"TurnDetail":      "turnDetail",
		"OpDetail":        "opDetail",
		"PayloadRef":      "payloadRef",
		"SourceItem":      "sourceItem",
		"HealthSource":    "healthSource",
		"LogItem":         "logItem",
	}
	return mapped(types, typeToStruct)
}

func mappedTSTypes(types []string) []string {
	return mapped(types, nil)
}

func mapped(types []string, aliases map[string]string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, typ := range types {
		if typ == "" {
			continue
		}
		if mapped, ok := aliases[typ]; ok {
			typ = mapped
		}
		if _, ok := seen[typ]; ok {
			continue
		}
		seen[typ] = struct{}{}
		out = append(out, typ)
	}
	return out
}

func collectGoJSONFields(dir string) (map[string]stringSet, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	wanted := toSet([]string{
		"sessionListItem", "sessionDetail", "childSummary", "turnDetail",
		"opDetail", "payloadRef", "sourceItem", "healthSource", "logItem",
		"traceOp", "compareResponse",
	})
	fields := map[string]stringSet{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, ok := wanted[ts.Name.Name]; !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				if _, ok := fields[ts.Name.Name]; !ok {
					fields[ts.Name.Name] = stringSet{}
				}
				for _, f := range st.Fields.List {
					if f.Tag == nil {
						continue
					}
					raw, err := strconv.Unquote(f.Tag.Value)
					if err != nil {
						return nil, fmt.Errorf("%s: invalid struct tag: %w", path, err)
					}
					jsonName := strings.Split(reflect.StructTag(raw).Get("json"), ",")[0]
					if jsonName == "" || jsonName == "-" {
						continue
					}
					fields[ts.Name.Name][jsonName] = struct{}{}
				}
			}
		}
	}
	return fields, nil
}

func collectTSFields(paths ...string) (map[string]stringSet, error) {
	fields := map[string]stringSet{}
	for _, path := range paths {
		fileFields, err := collectTSFieldsFromFile(path)
		if err != nil {
			return nil, err
		}
		for typ, typFields := range fileFields {
			if _, ok := fields[typ]; ok {
				return nil, fmt.Errorf("%s: duplicate exported TypeScript contract interface %s", path, typ)
			}
			fields[typ] = typFields
		}
	}
	return fields, nil
}

func collectTSFieldsFromFile(path string) (map[string]stringSet, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- SOW-0105 contract gate reads the repo-controlled frontend type path resolved from the repo root.
	if err != nil {
		return nil, err
	}
	fields := map[string]stringSet{}
	interfaceRE := regexp.MustCompile(`^\s*export\s+interface\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{?`)
	propRE := regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)(\?)?\s*:`)
	current := ""
	depth := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if current == "" {
			if m := interfaceRE.FindStringSubmatch(line); m != nil {
				current = m[1]
				fields[current] = stringSet{}
				depth = strings.Count(line, "{") - strings.Count(line, "}")
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "*") && !strings.HasPrefix(trimmed, "//") {
			if m := propRE.FindStringSubmatch(line); m != nil {
				fields[current][m[1]] = struct{}{}
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			current = ""
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return fields, nil
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func hasField(fields map[string]stringSet, typ, field string) bool {
	set, ok := fields[typ]
	if !ok {
		return false
	}
	_, ok = set[field]
	return ok
}

func allHaveField(fields map[string]stringSet, types []string, field string) bool {
	for _, typ := range types {
		if !hasField(fields, typ, field) {
			return false
		}
	}
	return true
}

func toSet(values []string) stringSet {
	set := stringSet{}
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func allowedEntities() stringSet {
	return toSet([]string{
		"session", "child_session", "turn", "op", "payload_ref",
		"payload_streaming", "source", "health", "log", "trace",
		"compare", "stats", "topology", "timeline", "related", "search",
		"subscription", "sse", "parse_error", "payload_kind",
	})
}

func allowedEntityKinds() stringSet {
	return toSet([]string{"session", "turn", "op", "payload_ref", "source", "health", "log", "endpoint", "ui", "gate"})
}

func allowedStates() stringSet {
	return toSet([]string{"exposed", "exposed-via-include", "missing-default", "missing-completely", "internal-only"})
}

func allowedIntents() stringSet {
	return toSet([]string{"primary_list", "detail", "debug_proof", "api_only", "internal_only"})
}

func allowedPrivacyClasses() stringSet {
	return toSet([]string{"public", "path_sensitive", "content_sensitive", "hash_linkable", "internal"})
}

func allowedAdapterPopulation() stringSet {
	return toSet([]string{"broad", "partial", "sparse", "none", "not_applicable"})
}

func allowedIndexStatuses() stringSet {
	return toSet([]string{"indexed", "partial_index", "rollup_indexed", "not_indexed", "not_applicable"})
}

func allowedEligibility() stringSet {
	return toSet([]string{"eligible", "excluded", "not_applicable"})
}

func allowedIncludeTokens() stringSet {
	return toSet([]string{"payload_refs", "proof", "cursors"})
}
