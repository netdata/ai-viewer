-- b_subagent_task: sub-agent linkage via BOTH mechanisms (AC#4).
--   1. session.parent_id: ses_child01.parent_id = ses_parent01 => the child maps
--      to Kind=sub_agent with ParentNativeID=ses_parent01 and RootNativeID=ses_parent01.
--   2. tool='task' with state.metadata.sessionId: the parent's task part names
--      ses_child01, so the mapper emits BOTH a session Op (kind=session,
--      ChildSessionNativeID=ses_child01, the topology parent) AND the tool Op
--      (kind=tool, name=task) in the same turn (adapter-opencode.md rule for
--      "tool where tool='task'").
-- The parent session row's time_updated (5000) is LOWER than the child's (7000)
-- so the session-table delta yields the parent tree first, then the child tree
-- (deterministic affected-session order). All ids/timestamps synthetic.

CREATE TABLE session (
	id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT,
	slug TEXT NOT NULL, directory TEXT NOT NULL, title TEXT NOT NULL,
	version TEXT NOT NULL, agent TEXT, model TEXT,
	cost REAL NOT NULL DEFAULT 0,
	tokens_input INTEGER NOT NULL DEFAULT 0,
	tokens_output INTEGER NOT NULL DEFAULT 0,
	tokens_reasoning INTEGER NOT NULL DEFAULT 0,
	tokens_cache_read INTEGER NOT NULL DEFAULT 0,
	tokens_cache_write INTEGER NOT NULL DEFAULT 0,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	time_archived INTEGER);

CREATE TABLE message (
	id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	data TEXT NOT NULL);

CREATE TABLE part (
	id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	data TEXT NOT NULL);

CREATE TABLE session_message (
	id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL,
	time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
	data TEXT NOT NULL);

INSERT INTO session
	(id, project_id, parent_id, slug, directory, title, version, agent, model, time_created, time_updated, time_archived)
VALUES
	('ses_parent01', 'prj_x', '', 'lead-fox', '/work/proj', 'Parent session', '1.0.0', 'general',
	 '{"id":"claude-x","providerID":"anthropic"}', 1000, 5000, NULL),
	('ses_child01', 'prj_x', 'ses_parent01', 'aide-fox', '/work/proj', 'Child session', '1.0.0', 'reviewer',
	 '{"id":"claude-x","providerID":"anthropic"}', 5000, 7000, NULL);

INSERT INTO message (id, session_id, time_created, time_updated, data)
VALUES
	('msg_p1', 'ses_parent01', 2000, 8000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.03,"tokens":{"input":200,"output":50,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":2000,"completed":8000},"finish":"stop"}'),
	('msg_c1', 'ses_child01', 5100, 7000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"reviewer","cost":0.01,"tokens":{"input":90,"output":20,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":5100,"completed":7000},"finish":"stop"}');

INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
VALUES
	('prt_p01', 'msg_p1', 'ses_parent01', 2100, 2100, '{"type":"step-start"}'),
	('prt_p02', 'msg_p1', 'ses_parent01', 3000, 7000, '{"type":"tool","callID":"call_p2","tool":"task","state":{"status":"completed","input":{"prompt":"review this"},"output":"child done","metadata":{"sessionId":"ses_child01"},"time":{"start":3000,"end":7000}}}'),
	('prt_p03', 'msg_p1', 'ses_parent01', 8000, 8000, '{"type":"step-finish","reason":"stop","cost":0.03,"tokens":{"input":200,"output":50,"reasoning":0,"cache":{"read":0,"write":0}}}'),
	('prt_c01', 'msg_c1', 'ses_child01', 5200, 5200, '{"type":"step-start"}'),
	('prt_c02', 'msg_c1', 'ses_child01', 5300, 5300, '{"type":"text","text":"child reply"}'),
	('prt_c03', 'msg_c1', 'ses_child01', 7000, 7000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":90,"output":20,"reasoning":0,"cache":{"read":0,"write":0}}}');
