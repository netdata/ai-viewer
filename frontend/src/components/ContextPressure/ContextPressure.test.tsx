import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ContextPressure } from './ContextPressure';

describe('ContextPressure', () => {
  it('renders nothing for unknown models', () => {
    const { container } = render(
      <ContextPressure model="unknown-model-v9" tokensIn={1000} tokensCacheRead={0} />,
    );
    expect(container.firstChild).toBeNull();
  });

  it('renders the percentage for known models', () => {
    render(<ContextPressure model="gpt-4o" tokensIn={64_000} tokensCacheRead={0} />);
    expect(screen.getByText('50%')).toBeInTheDocument();
  });

  it('treats tokens_cache_read as part of the consumed budget', () => {
    render(<ContextPressure model="gpt-4o" tokensIn={32_000} tokensCacheRead={32_000} />);
    // 64000 / 128000 = 50%
    expect(screen.getByText('50%')).toBeInTheDocument();
  });

  it('matches model name with a date suffix', () => {
    // gpt-4o-2024-05-13 should resolve to gpt-4o (128K).
    render(<ContextPressure model="gpt-4o-2024-05-13" tokensIn={64_000} tokensCacheRead={0} />);
    expect(screen.getByText('50%')).toBeInTheDocument();
  });

  it('uses warning tone at >= 70%', () => {
    render(<ContextPressure model="claude-3-5-sonnet" tokensIn={150_000} tokensCacheRead={0} />);
    const badge = screen.getByRole('status');
    expect(badge.className).toContain('text-status-running');
    // 150000/200000 = 75%
    expect(screen.getByText('75%')).toBeInTheDocument();
  });

  it('uses critical tone at >= 90%', () => {
    render(<ContextPressure model="claude-3-5-sonnet" tokensIn={190_000} tokensCacheRead={0} />);
    expect(screen.getByText('95%')).toBeInTheDocument();
    const badge = screen.getByRole('status');
    expect(badge.className).toContain('text-status-failed');
  });

  it('includes the model label and token count in the aria-label', () => {
    render(<ContextPressure model="gpt-4o" tokensIn={32_000} tokensCacheRead={0} />);
    expect(
      screen.getByRole('status', { name: /Context pressure: 25\.0% of GPT-4o \(128K\) window/i }),
    ).toBeInTheDocument();
  });
});
