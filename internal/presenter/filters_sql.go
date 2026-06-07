package presenter

import "strings"

//nolint:unparam // alias is a structural parameter shared with sibling SQL fragment helpers.
func (f sessionFilter) whereClause(alias string) (string, []any) {
	conds, args := f.dimensionConds(alias)
	if f.from != nil {
		conds = append(conds, alias+".start_ts >= ?")
		args = append(args, *f.from)
	}
	if f.to != nil {
		conds = append(conds, alias+".start_ts <= ?")
		args = append(args, *f.to)
	}
	return joinConds(conds, args)
}

func (f sessionFilter) whereClauseNoTimeWindow(alias string) (string, []any) {
	return joinConds(f.dimensionConds(alias))
}

func (f sessionFilter) dimensionConds(alias string) ([]string, []any) {
	builder := sessionDimensionBuilder{alias: alias}
	builder.addRootGroup(f.group)
	builder.addIn(alias+".agent_name", f.agents)
	builder.addIn(alias+".model", f.models)
	builder.addIn(alias+".status", f.status)
	builder.addIn(alias+".source_id", f.source)
	builder.addSearch(f.q)
	builder.addTools(f.tools)

	return builder.conds, builder.args
}

type sessionDimensionBuilder struct {
	alias string
	conds []string
	args  []any
}

func (b *sessionDimensionBuilder) addRootGroup(group string) {
	if group != groupRoot {
		return
	}
	b.conds = append(b.conds, b.alias+".kind = ?")
	b.args = append(b.args, "root")
}

func (b *sessionDimensionBuilder) addIn(col string, vals []string) {
	if c, a := inClause(col, vals); c != "" {
		b.conds = append(b.conds, c)
		b.args = append(b.args, a...)
	}
}

func (b *sessionDimensionBuilder) addSearch(q string) {
	if q == "" {
		return
	}
	b.conds = append(b.conds, b.alias+".agent_name LIKE ? ESCAPE '\\'")
	b.args = append(b.args, "%"+escapeLike(q)+"%")
}

func (b *sessionDimensionBuilder) addTools(tools []string) {
	if len(tools) == 0 {
		return
	}
	ph := placeholders(len(tools))
	b.conds = append(b.conds,
		"EXISTS (SELECT 1 FROM ops o WHERE o.session_id = "+b.alias+".id AND o.kind = 'tool' AND o.name IN ("+ph+"))")
	for _, tval := range tools {
		b.args = append(b.args, tval)
	}
}

func joinConds(conds []string, args []any) (string, []any) {
	if len(conds) == 0 {
		return "1=1", args
	}
	return strings.Join(conds, " AND "), args
}

func inClause(col string, vals []string) (string, []any) {
	if len(vals) == 0 {
		return "", nil
	}
	args := make([]any, len(vals))
	for i, v := range vals {
		args[i] = v
	}
	return col + " IN (" + placeholders(len(vals)) + ")", args
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
