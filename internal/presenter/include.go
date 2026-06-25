package presenter

import "strings"

type includeOptions struct {
	PayloadRefs bool
	Proof       bool
	Cursors     bool
}

func parseIncludeOptions(raw string, allowed map[string]struct{}) (includeOptions, error) {
	var out includeOptions
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	seen := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		token := strings.TrimSpace(part)
		if token == "" {
			return out, wrapBadFilter("include contains an empty token")
		}
		if _, ok := allowed[token]; !ok {
			return out, wrapBadFilter("unknown include token " + quoteKey(token))
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		switch token {
		case "payload_refs":
			out.PayloadRefs = true
		case "proof":
			out.Proof = true
		case "cursors":
			out.Cursors = true
		}
	}
	return out, nil
}

func includeAllow(tokens ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		out[token] = struct{}{}
	}
	return out
}

func requireProofPayloadRefs(in includeOptions) error {
	if in.Proof && !in.PayloadRefs {
		return wrapBadFilter("include proof requires payload_refs")
	}
	return nil
}
