-- g_nested_subagent: a THREE-level session tree (SOW-0005 P2.4 RootNativeID).
--   ses_groot   (root,       parent_id NULL)
--     └─ ses_gchild      (parent_id = ses_groot)
--          └─ ses_ggrand (parent_id = ses_gchild)
-- The grandchild's RootNativeID must be the TRUE tree root (ses_groot), NOT its
-- direct parent (ses_gchild): the loader walks the parent_id chain to the top
-- (resolveRootID) and injects it into the mapper. Each session has one minimal
-- COMPLETED assistant turn (step-start + step-finish, completed ts) so the turn
-- finalizes (P1.3) and the golden is non-empty. All ids/timestamps synthetic
-- (SOW-0005 R5). time_updated ordering (groot 3000 < gchild 5000 < ggrand 7000)
-- makes the session-table delta emit root → child → grandchild deterministically.

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
	time_archived INTEGER, time_compacting INTEGER);

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
	('ses_groot', 'prj_x', '', 'tall-tree', '/work/proj', 'Root session', '1.0.0', 'general',
	 '{"id":"claude-x","providerID":"anthropic"}', 1000, 3000, NULL),
	('ses_gchild', 'prj_x', 'ses_groot', 'mid-branch', '/work/proj', 'Child session', '1.0.0', 'reviewer',
	 '{"id":"claude-x","providerID":"anthropic"}', 3000, 5000, NULL),
	('ses_ggrand', 'prj_x', 'ses_gchild', 'leaf-node', '/work/proj', 'Grandchild session', '1.0.0', 'reviewer',
	 '{"id":"claude-x","providerID":"anthropic"}', 5000, 7000, NULL);

INSERT INTO message (id, session_id, time_created, time_updated, data)
VALUES
	('msg_gr', 'ses_groot', 1100, 3000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"general","cost":0.01,"tokens":{"input":100,"output":20,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1100,"completed":3000},"finish":"stop"}'),
	('msg_gc', 'ses_gchild', 3100, 5000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"reviewer","cost":0.01,"tokens":{"input":90,"output":18,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":3100,"completed":5000},"finish":"stop"}'),
	('msg_gg', 'ses_ggrand', 5100, 7000,
	 '{"role":"assistant","providerID":"anthropic","modelID":"claude-x","agent":"reviewer","cost":0.01,"tokens":{"input":80,"output":16,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":5100,"completed":7000},"finish":"stop"}');

INSERT INTO part (id, message_id, session_id, time_created, time_updated, data)
VALUES
	('prt_gr1', 'msg_gr', 'ses_groot', 1200, 1200, '{"type":"step-start"}'),
	('prt_gr2', 'msg_gr', 'ses_groot', 3000, 3000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":100,"output":20,"reasoning":0,"cache":{"read":0,"write":0}}}'),
	('prt_gc1', 'msg_gc', 'ses_gchild', 3200, 3200, '{"type":"step-start"}'),
	('prt_gc2', 'msg_gc', 'ses_gchild', 5000, 5000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":90,"output":18,"reasoning":0,"cache":{"read":0,"write":0}}}'),
	('prt_gg1', 'msg_gg', 'ses_ggrand', 5200, 5200, '{"type":"step-start"}'),
	('prt_gg2', 'msg_gg', 'ses_ggrand', 7000, 7000, '{"type":"step-finish","reason":"stop","cost":0.01,"tokens":{"input":80,"output":16,"reasoning":0,"cache":{"read":0,"write":0}}}');
