package aiagent_v2

// firstLLMModel walks the opTree depth-first and returns the first
// `attributes.model` it finds on an LLM op. v2 sets the model on the
// op's attribute bag (and replicates it on the accounting entry); we
// prefer the attribute so the model is discoverable even on ops that
// failed before producing an accounting row. The walk uses the same
// child-session depth cap as event emission.
func firstLLMModel(node opTree, depth int) string {
	for i := range node.Turns {
		if m := firstLLMModelFromOps(node.Turns[i].Ops, depth); m != "" {
			return m
		}
	}
	for i := range node.Steps {
		if m := firstLLMModelFromOps(node.Steps[i].Ops, depth); m != "" {
			return m
		}
	}
	return ""
}

func firstLLMModelFromOps(ops []operationNode, depth int) string {
	for i := range ops {
		op := ops[i]
		if op.Kind == "llm" {
			if m := attrString(op.Attributes, "model"); m != "" {
				return m
			}
			// Fallback to accounting entry when attributes omit model.
			if len(op.Accounting) > 0 && op.Accounting[0].Model != "" {
				return op.Accounting[0].Model
			}
		}
		if op.ChildSession != nil {
			if m := firstLLMModelFromChild(*op.ChildSession, depth); m != "" {
				return m
			}
		}
	}
	return ""
}

func firstLLMModelFromChild(child opTree, parentDepth int) string {
	childDepth := parentDepth + 1
	if childDepth > maxChildSessionDepth {
		return ""
	}
	return firstLLMModel(child, childDepth)
}
