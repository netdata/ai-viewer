import { test, expect, type APIRequestContext } from '@playwright/test';

async function richestSessionId(request: APIRequestContext): Promise<string> {
  const resp = await request.get('/api/sessions');
  expect(resp.ok()).toBeTruthy();
  const body = (await resp.json()) as {
    items: Array<{ id: string; op_count: number }>;
  };
  expect(body.items.length).toBeGreaterThanOrEqual(1);
  return [...body.items].sort((a, b) => b.op_count - a.op_count)[0]!.id;
}

test.describe('session detail layout', () => {
  test('renders as one coherent resizable workbench', async ({ page, request }) => {
    const id = await richestSessionId(request);
    await page.setViewportSize({ width: 1600, height: 1000 });
    await page.goto(`/sessions/${encodeURIComponent(id)}`);

    await expect(page.getByRole('region', { name: /waterfall visualization/i })).toBeVisible();
    await expect(page.getByRole('region', { name: /events panel/i })).toBeVisible();
    await expect(page.getByRole('region', { name: /^turn view$/i })).toBeVisible();

    const metrics = await page.evaluate(() => {
      const rectFor = (selector: string) => {
        const el = document.querySelector(selector);
        if (el === null) return null;
        const rect = el.getBoundingClientRect();
        return {
          left: rect.left,
          top: rect.top,
          right: rect.right,
          bottom: rect.bottom,
          width: rect.width,
          height: rect.height,
        };
      };
      const handleRects = Array.from(document.querySelectorAll('[data-panel-resize-handle-id]'))
        .map((el) => {
          const rect = el.getBoundingClientRect();
          return { width: rect.width, height: rect.height };
        });
      return {
        iframeCount: document.querySelectorAll('iframe').length,
        rootCount: document.querySelectorAll('#root').length,
        handles: handleRects,
        bottomPanel: rectFor('[aria-label="events panel"]'),
        eventScroller: rectFor('[aria-label="Event list scroll area"]'),
      };
    });

    expect(metrics.iframeCount).toBe(0);
    expect(metrics.rootCount).toBe(1);
    expect(metrics.handles.length).toBeGreaterThanOrEqual(2);
    for (const handle of metrics.handles) {
      expect(handle.width).toBeGreaterThan(0);
      expect(handle.height).toBeGreaterThan(0);
    }
    expect(metrics.bottomPanel).not.toBeNull();
    expect(metrics.eventScroller).not.toBeNull();
    expect(metrics.eventScroller!.bottom).toBeLessThanOrEqual(metrics.bottomPanel!.bottom + 1);
    expect(metrics.eventScroller!.top).toBeGreaterThanOrEqual(metrics.bottomPanel!.top - 1);
  });
});
