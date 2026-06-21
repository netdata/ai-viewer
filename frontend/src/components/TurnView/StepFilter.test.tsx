// StepFilter (SOW-0090 chunk 9) — pure logic + render tests.

import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StepFilter, FILTER_KINDS, matchStepFilter, type StepKindFilter } from './StepFilter';

describe('matchStepFilter', () => {
  it("'all' matches every kind", () => {
    expect(matchStepFilter('all', 'tool', 'read_file')).toBe(true);
    expect(matchStepFilter('all', 'reasoning', '')).toBe(true);
    expect(matchStepFilter('all', 'internal', 'user_input')).toBe(true);
  });

  it("'user' is an alias for internal + name=user_input", () => {
    expect(matchStepFilter('user', 'internal', 'user_input')).toBe(true);
    expect(matchStepFilter('user', 'internal', 'something_else')).toBe(false);
    expect(matchStepFilter('user', 'tool', 'user_input')).toBe(false);
  });

  it('kind-specific filters match by kind only', () => {
    expect(matchStepFilter('tool', 'tool', 'read_file')).toBe(true);
    expect(matchStepFilter('tool', 'tool', 'write_file')).toBe(true);
    expect(matchStepFilter('tool', 'reasoning', '')).toBe(false);
    expect(matchStepFilter('reasoning', 'reasoning', '')).toBe(true);
    expect(matchStepFilter('reasoning', 'tool', 'reasoning')).toBe(false);
    expect(matchStepFilter('assistant', 'llm', 'message')).toBe(true);
    expect(matchStepFilter('assistant', 'llm', 'tool_use')).toBe(false);
  });
});

describe('StepFilter render', () => {
  const counts: Record<StepKindFilter, number> = {
    all: 31,
    user: 1,
    reasoning: 5,
    assistant: 5,
    tool: 17,
    session: 1,
    compaction: 2,
    internal: 1,
    llm: 5,
    generic: 0,
    system: 0,
  };

  it('renders one pill per filter kind with the count badge', () => {
    render(<StepFilter active="all" counts={counts} onChange={() => {}} />);
    for (const entry of FILTER_KINDS) {
      const pill = screen.getByRole('tab', { name: new RegExp(entry.label, 'i') });
      expect(pill).toBeInTheDocument();
      expect(within(pill).getByText(String(counts[entry.value]))).toBeInTheDocument();
    }
  });

  it('marks the active pill with data-active=true', () => {
    render(<StepFilter active="tool" counts={counts} onChange={() => {}} />);
    const toolPill = screen.getByRole('tab', { name: /tool/i });
    expect(toolPill.getAttribute('data-active')).toBe('true');
    const allPill = screen.getByRole('tab', { name: /all/i });
    expect(allPill.getAttribute('data-active')).toBe('false');
  });

  it('invokes onChange with the clicked kind', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<StepFilter active="all" counts={counts} onChange={onChange} />);
    await user.click(screen.getByRole('tab', { name: /reasoning/i }));
    expect(onChange).toHaveBeenCalledWith('reasoning');
    await user.click(screen.getByRole('tab', { name: /tool/i }));
    expect(onChange).toHaveBeenCalledWith('tool');
  });
});
