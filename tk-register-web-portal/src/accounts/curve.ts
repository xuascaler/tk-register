import type { AccountTrendPoint } from './types';

export type ChartPoint = AccountTrendPoint & { time: number };
export type XY = { x: number; y: number };
export type Cubic = { start: XY; end: XY; first: XY; second: XY };

// Keep server-side segment boundaries. Invalid samples break a segment rather
// than silently joining observations across unavailable data.
export function chartSegments(segments: AccountTrendPoint[][]): ChartPoint[][] {
  const result: ChartPoint[][] = [];
  for (const segment of segments) {
    let current: ChartPoint[] = [];
    for (const point of segment) {
      const time = Date.parse(point.collected_at);
      if (!Number.isFinite(time) || !Number.isFinite(point.value)) {
        if (current.length) result.push(current);
        current = [];
        continue;
      }
      const previous = current.at(-1);
      if (previous && (time <= previous.time || point.value < previous.value)) {
        result.push(current);
        current = [];
      }
      current.push({ ...point, time });
    }
    if (current.length) result.push(current);
  }
  return result;
}

// Monotone cubic Hermite interpolation, with Fritsch–Carlson slope limiting.
// Control points remain between adjacent sample values, so smoothing cannot
// invent a new peak, dip, or negative cumulative amount.
export function monotoneCubics(points: XY[]): Cubic[] {
  if (points.length < 2) return [];
  const widths = points.slice(1).map((point, i) => point.x - points[i].x);
  if (points.some(point => !Number.isFinite(point.x) || !Number.isFinite(point.y)) || widths.some(width => width <= 0)) return [];
  const slopes = widths.map((width, i) => (points[i + 1].y - points[i].y) / width);
  const tangents = points.map((_, i) => {
    if (i === 0) return slopes[0];
    if (i === points.length - 1) return slopes.at(-1)!;
    const before = slopes[i - 1], after = slopes[i];
    if (before === 0 || after === 0 || Math.sign(before) !== Math.sign(after)) return 0;
    const first = 2 * widths[i] + widths[i - 1];
    const second = widths[i] + 2 * widths[i - 1];
    return (first + second) / (first / before + second / after);
  });
  for (let i = 0; i < slopes.length; i++) {
    if (slopes[i] === 0) { tangents[i] = 0; tangents[i + 1] = 0; continue; }
    const a = tangents[i] / slopes[i], b = tangents[i + 1] / slopes[i];
    const magnitude = Math.hypot(a, b);
    if (magnitude > 3) {
      const scale = 3 / magnitude;
      tangents[i] = scale * a * slopes[i];
      tangents[i + 1] = scale * b * slopes[i];
    }
  }
  return widths.map((width, i) => ({
    start: points[i], end: points[i + 1],
    first: { x: points[i].x + width / 3, y: points[i].y + tangents[i] * width / 3 },
    second: { x: points[i + 1].x - width / 3, y: points[i + 1].y - tangents[i + 1] * width / 3 },
  }));
}

export function curvePath(points: XY[]): string {
  const curves = monotoneCubics(points);
  if (!curves.length) return '';
  const pair = (point: XY) => `${point.x.toFixed(3)},${point.y.toFixed(3)}`;
  return `M${pair(curves[0].start)} ${curves.map(curve => `C${pair(curve.first)} ${pair(curve.second)} ${pair(curve.end)}`).join(' ')}`;
}

// Hover targets are actual observations, never interpolated values. A cursor in
// a missing-data interval has no selection, rather than snapping across a gap.
export function hoverPoint(segments: ChartPoint[][], time: number, tolerance: number): ChartPoint | null {
  for (const segment of segments) {
    if (!segment.length) continue;
    if (segment.length === 1) {
      if (Math.abs(segment[0].time - time) <= tolerance) return segment[0];
      continue;
    }
    if (time < segment[0].time || time > segment.at(-1)!.time) continue;
    let lo = 0, hi = segment.length - 1;
    while (lo < hi) {
      const mid = Math.floor((lo + hi) / 2);
      if (segment[mid].time < time) lo = mid + 1; else hi = mid;
    }
    if (lo > 0 && time - segment[lo - 1].time <= segment[lo].time - time) lo--;
    return segment[lo];
  }
  return null;
}
