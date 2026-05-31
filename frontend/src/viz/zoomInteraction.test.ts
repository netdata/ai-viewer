import { afterEach, describe, expect, it, vi } from 'vitest';
import { attachZoom, SCALE_EXTENT, zoomEventFilter } from './zoomInteraction';

// viz/zoomInteraction.ts is the shared d3-zoom wiring for the time-axis viz
// surfaces (Timeline tab + Detailed Waterfall). These tests pin the wheel
// convention at the filter layer (the SOW-0006 contract: shift/ctrl wheel zooms,
// plain wheel does not) and the plainWheelPan switch that distinguishes the two
// consumers: the Timeline intercepts a plain wheel to PAN (preventDefault), while
// the Waterfall leaves a plain wheel to the native vertical scroller (NOT
// prevented), so its rows scroll while only the time track zooms/pans.

describe('zoomEventFilter', () => {
  it('rejects a plain wheel (plain wheel must not zoom)', () => {
    const e = new WheelEvent('wheel', { deltaY: 40 });
    expect(zoomEventFilter(e)).toBe(false);
  });

  it('allows a shift+wheel (zoom)', () => {
    const e = new WheelEvent('wheel', { deltaY: 40, shiftKey: true });
    expect(zoomEventFilter(e)).toBe(true);
  });

  it('allows a ctrl+wheel (pinch / browser-zoom gesture)', () => {
    const e = new WheelEvent('wheel', { deltaY: 40, ctrlKey: true });
    expect(zoomEventFilter(e)).toBe(true);
  });

  it('rejects a mousedown with a null view (jsdom synthetic — d3-drag nodrag would deref event.view)', () => {
    // jsdom dispatches synthetic mouse events with view === null; the filter must
    // reject so d3-zoom never dereferences event.view.document (a no-op in a real
    // browser, which always carries a view).
    const e = new MouseEvent('mousedown', { button: 0 });
    expect((e as MouseEvent & { view: unknown }).view).toBeNull();
    expect(zoomEventFilter(e)).toBe(false);
  });

  it('allows a primary-button mousedown that carries a view (drag-pan)', () => {
    // A real browser mousedown carries a non-null view. Synthesize that shape.
    const e = new MouseEvent('mousedown', { button: 0 });
    Object.defineProperty(e, 'view', { value: window, configurable: true });
    expect(zoomEventFilter(e)).toBe(true);
  });

  it('rejects a non-primary (secondary) button mousedown', () => {
    const e = new MouseEvent('mousedown', { button: 2 });
    Object.defineProperty(e, 'view', { value: window, configurable: true });
    expect(zoomEventFilter(e)).toBe(false);
  });

  it('lets a non-mouse, non-wheel event through (e.g. touch/keyboard)', () => {
    const e = new Event('touchstart');
    expect(zoomEventFilter(e)).toBe(true);
  });

  it('exposes the shared SCALE_EXTENT default [0.2, 64]', () => {
    expect(SCALE_EXTENT).toEqual([0.2, 64]);
  });
});

describe('attachZoom plainWheelPan switch', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('plainWheelPan:true preventDefaults a plain wheel (it pans, not scrolls the page)', () => {
    const surface = document.createElement('div');
    document.body.appendChild(surface);
    const { dispose } = attachZoom(surface, () => {}, { plainWheelPan: true });
    const wheel = new WheelEvent('wheel', { deltaX: 30, deltaY: 0, bubbles: true, cancelable: true });
    surface.dispatchEvent(wheel);
    // The pan listener consumes the plain wheel.
    expect(wheel.defaultPrevented).toBe(true);
    dispose();
    surface.remove();
  });

  it('plainWheelPan:false does NOT preventDefault a plain wheel (it bubbles to the native scroller)', () => {
    const surface = document.createElement('div');
    document.body.appendChild(surface);
    const { dispose } = attachZoom(surface, () => {}, { plainWheelPan: false });
    const wheel = new WheelEvent('wheel', { deltaX: 30, deltaY: 0, bubbles: true, cancelable: true });
    surface.dispatchEvent(wheel);
    // No pan listener: the plain wheel is left to the native scroller.
    expect(wheel.defaultPrevented).toBe(false);
    dispose();
    surface.remove();
  });

  it('dispose() removes the zoom wiring (idempotent, no throw)', () => {
    const surface = document.createElement('div');
    document.body.appendChild(surface);
    const { dispose } = attachZoom(surface, () => {}, { plainWheelPan: true });
    expect(() => {
      dispose();
    }).not.toThrow();
    surface.remove();
  });
});
