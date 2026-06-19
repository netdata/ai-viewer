import { select } from 'd3-selection';
import { zoom, zoomIdentity, type D3ZoomEvent, type ZoomBehavior } from 'd3-zoom';

// Shared d3-zoom interaction for the time-axis viz surfaces (Timeline tab,
// Detailed Waterfall). Lives in viz/ so the React renderers consume ONE wheel
// convention and never reimplement the filter/pan wiring (no drift between
// tabs). The D3 boundary holds: this module owns the d3-zoom behavior + event
// filtering; the renderers only translate the resulting transform into a paint
// (an SVG matrix or a Canvas redraw).
//
// The SOW-0006 wheel convention:
//   - SHIFT+wheel (and ctrl+wheel) ZOOMS — allowed through to d3-zoom;
//   - PLAIN wheel either PANS (Timeline: plainWheelPan ON) or is left to the
//     native scroller (Waterfall: plainWheelPan OFF, so a plain wheel scrolls
//     the rows vertically while only the time track zooms/pans);
//   - primary-button drag PANS.

/**
 * SCALE_EXTENT is the default zoom range for the time-axis surfaces: from 0.2×
 * (zoomed out) to 64× (zoomed in). Shared so the Timeline and the Detailed
 * Waterfall clamp to the same limits unless a caller overrides it.
 */
export const SCALE_EXTENT: [number, number] = [0.2, 64];

/** Options for attachZoom. */
export interface AttachZoomOpts {
  /** Zoom clamp range. Defaults to SCALE_EXTENT. */
  scaleExtent?: [number, number];
  /**
   * When true (default), a PLAIN wheel pans via a non-passive wheel listener
   * (the video-editor feel — Timeline tab). When false, a plain wheel is NOT
   * intercepted: the filter rejects it WITHOUT preventDefault so it bubbles to
   * the native scroller (the Detailed Waterfall, whose rows scroll natively
   * while only the time track zooms/pans).
   */
  plainWheelPan?: boolean;
}

/**
 * zoomEventFilter implements the SOW-0006 wheel convention at the d3-zoom layer:
 * a PLAIN wheel must NOT zoom (it either pans via a separate listener, or is
 * left to the native scroller), so we reject it here; a SHIFT+wheel zooms
 * (allowed through). Primary-button drag pans. It also mirrors TopologyRenderer's
 * hardening: d3-zoom's mousedown handler dereferences event.view.document (via
 * d3-drag's nodrag), and the jsdom test environment dispatches synthetic pointer
 * events with a null view — so a view-less mousedown is rejected (a no-op in a
 * real browser, which always carries a view; never a silent crash).
 */
export function zoomEventFilter(event: Event): boolean {
  const e = event as Event & { view?: unknown; button?: number; shiftKey?: boolean; ctrlKey?: boolean };
  if (e.type === 'wheel') {
    // Only shift+wheel reaches d3-zoom (→ zoom). Plain wheel is handled as pan
    // (or left to the native scroller). ctrl+wheel (pinch-zoom gesture / browser
    // zoom) is also allowed to zoom.
    return e.shiftKey === true || e.ctrlKey === true;
  }
  if (e.type === 'mousedown' && e.view == null) {
    return false;
  }
  // Primary button only; let non-mouse events through.
  return e.button === undefined || e.button === 0;
}

/**
 * attachZoom wires a d3-zoom behavior to a surface element and returns it plus a
 * disposer. When plainWheelPan is on (default), the plain-wheel-pans behavior is
 * added as a separate non-passive wheel listener that calls
 * zoomBehavior.translateBy, so panning flows through the SAME transform/event
 * pipeline as drag + shift-wheel-zoom (one source of truth for the transform).
 * When plainWheelPan is off, no such listener is added: a plain wheel is left to
 * the native scroller (the filter rejects it without preventDefault). Shared by
 * the SVG and Canvas paths so the interaction is defined once.
 */
export function attachZoom<E extends Element>(
  surface: E,
  onZoom: (_event: D3ZoomEvent<E, unknown>) => void,
  opts?: AttachZoomOpts,
): { behavior: ZoomBehavior<E, unknown>; dispose: () => void } {
  const scaleExtent = opts?.scaleExtent ?? SCALE_EXTENT;
  const plainWheelPan = opts?.plainWheelPan ?? true;
  const behavior = zoom<E, unknown>()
    .scaleExtent(scaleExtent)
    .filter(zoomEventFilter)
    .on('zoom', onZoom);
  const selection = select(surface);
  selection.call(behavior);
  // Start un-panned/un-zoomed (d3's documented selection.call idiom).
  selection.call((s) => { behavior.transform(s, zoomIdentity); });

  if (!plainWheelPan) {
    // No plain-wheel-pan listener: a plain wheel is rejected by the filter
    // WITHOUT preventDefault, so it reaches the native scroller (Waterfall rows
    // scroll vertically; only the time track zooms/pans via shift+wheel/drag).
    return {
      behavior,
      dispose: () => {
        selection.on('.zoom', null);
      },
    };
  }

  // Plain wheel → horizontal/vertical pan (video-editor feel). Non-passive so we
  // can preventDefault and stop the page from scrolling. Shift/ctrl wheel is left
  // to d3-zoom (it zooms via the filter above). Typed as the base Event (the
  // generic `Element` surface has no `wheel` entry in its event map) and narrowed
  // to WheelEvent — it always is one for a 'wheel' listener.
  const onWheel = (evt: Event): void => {
    const ev = evt as WheelEvent;
    if (ev.shiftKey || ev.ctrlKey) {
      return;
    }
    ev.preventDefault();
    // deltaX pans horizontally; a plain vertical wheel scrubs the time axis too
    // when there is no horizontal delta (the natural timeline feel), otherwise it
    // pans vertically across lanes. translateBy is in pre-scale units, so it pans
    // consistently at any zoom level.
    const dx = ev.deltaX !== 0 ? ev.deltaX : ev.deltaY;
    const dy = ev.deltaX !== 0 ? ev.deltaY : 0;
    behavior.translateBy(selection, -dx, -dy);
  };
  surface.addEventListener('wheel', onWheel, { passive: false });

  return {
    behavior,
    dispose: () => {
      surface.removeEventListener('wheel', onWheel);
      selection.on('.zoom', null);
    },
  };
}
