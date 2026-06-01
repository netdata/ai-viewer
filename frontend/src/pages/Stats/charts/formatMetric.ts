import { formatCost, formatDuration, formatNumber } from '../../../lib/format';

// formatMetricValue routes a chart value to the right unit for the selected
// stats metric (rest-api.md enum: cost|tokens_in|tokens_out|calls|failures|
// duration_us). Reuses the shared lib/format helpers so the dashboard renders
// values identically to the rest of the app:
//   - cost        -> USD ($x.xx, with sub-cent precision)
//   - tokens/calls/failures -> thousands-separated integer
//   - duration_us -> human duration ("2s", "1m 30s", …)
//   - unknown     -> plain thousands-separated number (open-enum safe; the
//     client never crashes on a future metric — AGENTS.md treats enums as open).

/** formatMetricValue formats a numeric chart value per the metric's unit. */
export function formatMetricValue(metric: string, value: number): string {
  switch (metric) {
    case 'cost':
      return formatCost(value);
    case 'duration_us':
      return formatDuration(value);
    case 'tokens_in':
    case 'tokens_out':
    case 'calls':
    case 'failures':
      return formatNumber(value);
    default:
      return formatNumber(value);
  }
}
