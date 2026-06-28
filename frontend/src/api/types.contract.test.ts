import { describe, expect, it } from 'vitest';
import type {
  ChildSummary,
  CompareResponse,
  HealthSource,
  LogItem,
  OpDetail,
  PayloadRef,
  SessionDetail,
  SessionListItem,
  SourceItem,
  TurnDetail,
} from './types';

type OptionalKeys<T> = {
  [K in keyof T]-?: Record<never, never> extends Pick<T, K> ? K : never;
}[keyof T];

type IsRequired<T, K extends keyof T> = K extends OptionalKeys<T> ? false : true;

function expectRequiredField<T extends true>(value: T): void {
  expect(value).toBe(true);
}

describe('API field contract types', () => {
  it('locks required fields that mirror always-emitted presenter JSON fields', () => {
    expectRequiredField<IsRequired<SessionListItem, 'effective_status'>>(true);
    expectRequiredField<IsRequired<SessionListItem, 'error_class'>>(true);
    expectRequiredField<IsRequired<SessionListItem, 'last_activity_ts'>>(true);
    expectRequiredField<IsRequired<SessionDetail, 'effective_status'>>(true);
    expectRequiredField<IsRequired<SessionDetail, 'last_activity_ts'>>(true);
    expectRequiredField<IsRequired<PayloadRef, 'op_id'>>(true);
  });

  it('types list/session/child fields from the field matrix', () => {
    const listItem: SessionListItem = {
      id: 's1',
      native_id: 'native',
      root_session_id: 's1',
      parent_session_id: null,
      source_id: 'src1',
      kind: 'root',
      agent_name: 'agent',
      model: 'model',
      provider: 'anthropic',
      status: 'completed',
      effective_status: 'completed',
      error_class: 'runtime_error',
      start_ts: 1,
      end_ts: 2,
      last_activity_ts: 2,
      tokens_in: 10,
      tokens_out: 20,
      cost_usd: 0.1,
      turn_count: 1,
      op_count: 2,
      failure_count: 0,
      child_session_count: 1,
    };

    const detail: SessionDetail = {
      id: 's1',
      native_id: 'native',
      root_session_id: 's1',
      parent_session_id: null,
      source_id: 'src1',
      kind: 'root',
      agent_name: 'agent',
      model: 'model',
      provider: 'anthropic',
      provider_alias: 'claude',
      cwd: '/workspace/project',
      call_path: 'root>child',
      status: 'completed',
      effective_status: 'completed',
      error_class: null,
      error_message: null,
      start_ts: 1,
      end_ts: 2,
      last_activity_ts: 2,
      duration_us: 1,
      first_user_message_hash: 'abc123',
      tokens_in: 10,
      tokens_out: 20,
      tokens_cache_read: 3,
      tokens_cache_write: 4,
      cost_usd: 0.1,
      turn_count: 1,
      op_count: 2,
      failure_count: 0,
      child_session_count: 1,
    };

    const child: ChildSummary = {
      id: 'c1',
      native_id: 'native-child',
      kind: 'sub_agent',
      agent_name: 'child-agent',
      model: 'child-model',
      provider: 'anthropic',
      status: 'failed',
      error_class: 'tool_error',
      start_ts: 3,
      end_ts: 4,
      tokens_in: 1,
      tokens_out: 2,
      cost_usd: 0.01,
      op_count: 1,
      failure_count: 1,
    };

    const compare: CompareResponse = {
      sessions: [listItem],
      summary: {
        duration_us: { per_session: {} },
        cost_usd: { per_session: {} },
        op_count: { per_session: {} },
        tokens: { per_session: {} },
      },
      tool_usage: { common: [], added: {}, removed: {}, per_session: {} },
      errors: { common: [], only_in: {} },
      models: { shared: [], diverged: {} },
      agents: { shared: [], diverged: {} },
      kind_distribution: { per_session: {} },
    };

    expect(detail.cwd).toBe('/workspace/project');
    expect(child.provider).toBe('anthropic');
    expect(compare.sessions[0]?.provider).toBe('anthropic');
  });

  it('types turn/op/payload proof fields from the field matrix', () => {
    const payload: PayloadRef = {
      id: 42,
      op_id: 'op1',
      kind: 'sdk_request',
      artifact_class: 'llm_sdk_request',
      format: 'json',
      compression: null,
      original_bytes: 100,
      stored_bytes: 80,
      location_uri: 'file:///payload.json',
      sha256: null,
    };

    const op: OpDetail = {
      id: 'op1',
      kind: 'llm',
      name: 'message',
      model: 'model',
      provider: 'anthropic',
      provider_alias: 'claude',
      tool_namespace: 'fs',
      reasoning_kind: 'summary',
      bytes_in: 10,
      bytes_out: 20,
      chars_in: 5,
      chars_out: 15,
      parent_op_id: null,
      start_ts: 1,
      end_ts: 2,
      duration_us: 1,
      status: 'completed',
      error_class: null,
      error_message: null,
      tokens_in: 10,
      tokens_out: 20,
      tokens_cache_read: 3,
      tokens_cache_write: 4,
      cost_usd: 0.1,
      ctx_used: null,
      ctx_max: null,
      child_session_id: null,
      payload_refs: [payload],
    };

    const turn: TurnDetail = {
      id: 't1',
      seq: 1,
      start_ts: 1,
      end_ts: 2,
      status: 'completed',
      error_class: null,
      tokens_in: 10,
      tokens_out: 20,
      tokens_cache_read: 3,
      tokens_cache_write: 4,
      cost_usd: 0.1,
      op_count: 1,
      ops: [op],
    };

    expect(turn.ops[0]?.payload_refs?.[0]?.artifact_class).toBe('llm_sdk_request');
  });

  it('types source, health, and log metadata fields from the field matrix', () => {
    const source: SourceItem = {
      id: 'src1',
      format: 'aiagent_v3',
      location: '/sources/src1',
      enabled: true,
      parse_errors: 0,
      last_seen_at: 1,
      created_at: 1,
      cursor: 'cursor',
      last_seq: 2,
      last_ts_us: 3,
      updated_at: 4,
      progress_updated_at: 4,
      lifecycle_state: 'tailing',
      lifecycle_state_at: 4,
      scan_started_at: 1,
      scan_completed_at: 2,
      tail_started_at: 3,
      tail_heartbeat_at: 4,
      tail_restart_count: 0,
      read_model_state: 'ready',
      read_model_state_at: 4,
      read_model_repair_attempts: 0,
      meta: { adapter_version: '3' },
    };
    const health: HealthSource = {
      id: 'src1',
      format: 'aiagent_v3',
      location: '/sources/src1',
      enabled: true,
      last_seen_at: 1,
      lag_us: 10,
      parse_errors: 0,
      last_seq: 2,
      progress_updated_at: 4,
      lifecycle_state: 'tailing',
      lifecycle_state_at: 4,
      scan_started_at: 1,
      scan_completed_at: 2,
      tail_started_at: 3,
      tail_heartbeat_at: 4,
      tail_restart_count: 0,
      read_model_state: 'ready',
      read_model_state_at: 4,
      read_model_repair_attempts: 0,
      meta: { adapter_version: '3' },
    };
    const log: LogItem = {
      ts: 1,
      severity: 'INF',
      source: 'presenter',
      op_id: null,
      message: 'hello',
      extras: { request_id: 'r1' },
    };

    expect(source.meta?.adapter_version).toBe('3');
    expect(health.meta?.adapter_version).toBe('3');
    expect(log.extras?.request_id).toBe('r1');
  });
});
