import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { LogRow } from './LogRow';
import type { LogItem } from '../../api/types';

// LogRow renders one log entry. The severity is shown as TEXT (not color alone)
// and an absent op_id / empty source render an em dash rather than blank.

function renderRow(entry: LogItem) {
  // A row must live inside a table for valid DOM.
  return render(
    <table>
      <tbody>
        <LogRow entry={entry} />
      </tbody>
    </table>,
  );
}

const BASE: LogItem = {
  ts: 1_700_000_000_000_000,
  severity: 'WRN',
  source: 'aiagent_v3',
  op_id: 'op-1',
  message: 'something happened',
  extras: null,
};

describe('LogRow', () => {
  it('renders severity text, source, op id and message', () => {
    renderRow(BASE);
    expect(screen.getByText('WRN')).toBeInTheDocument();
    expect(screen.getByText('aiagent_v3')).toBeInTheDocument();
    expect(screen.getByText('op-1')).toBeInTheDocument();
    expect(screen.getByText('something happened')).toBeInTheDocument();
  });

  it.each([
    ['ERR', /sevErr/],
    ['WRN', /sevWrn/],
    ['INF', /sevInf/],
    ['DBG', /sevDbg/],
  ] as const)('maps %s severity to its visual class', (severity, className) => {
    renderRow({ ...BASE, severity });
    expect(screen.getByText(severity).className).toMatch(className);
  });

  it('renders an em dash for a null op_id', () => {
    renderRow({ ...BASE, op_id: null });
    // The message cell still renders; the op-id cell shows the dash.
    expect(screen.getByText('something happened')).toBeInTheDocument();
    expect(screen.getAllByText('—').length).toBeGreaterThan(0);
  });

  it('renders an em dash for an empty source', () => {
    renderRow({ ...BASE, source: '', op_id: null });
    expect(screen.getAllByText('—').length).toBeGreaterThanOrEqual(2);
  });

  it('renders an unknown severity verbatim', () => {
    renderRow({ ...BASE, severity: 'TRACE' });
    expect(screen.getByText('TRACE')).toBeInTheDocument();
  });
});
