import assert from 'node:assert/strict';
import test from 'node:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { chartSegments, curvePath, hoverPoint, monotoneCubics, type XY } from '../src/accounts/curve.ts';
import { validSyncInterval } from '../src/accounts/helpers.ts';
import { UsageTrend } from '../src/accounts/UsageTrend.tsx';
import type { AccountDetails, AccountTrendPoint } from '../src/accounts/types.ts';

function observation(minute: number, value: number): AccountTrendPoint {
  return { snapshot_id: String(minute), collected_at: new Date(Date.UTC(2026, 10, 4, 0, minute)).toISOString(), value };
}
function bezier(a: number, b: number, c: number, d: number, t: number) {
  return (1 - t) ** 3 * a + 3 * (1 - t) ** 2 * t * b + 3 * (1 - t) * t ** 2 * c + t ** 3 * d;
}

test('sync interval accepts whole seconds without interpreting 300 as hours', () => {
  for (const value of [60, 61, 300, 3600, 604800]) assert.equal(validSyncInterval(value), true);
  for (const value of [0, 5, 59, 604801, 299.5, NaN, Infinity]) assert.equal(validSyncInterval(value), false);
});

test('smooth curves preserve sample endpoints, plateaus, and monotonicity without overshoot', () => {
  for (const values of [[0, 10, 10, 11, 90], [90, 11, 10, 10, 0], [0, 0, 0, 0, 0], [10, 90, 11, 10, 30]]) {
    const points: XY[] = values.map((value, i) => ({ x: [0, 1, 50, 51, 100][i], y: value }));
    const cubics = monotoneCubics(points);
    assert.equal(cubics.length, points.length - 1);
    cubics.forEach((curve, i) => {
      assert.deepEqual(curve.start, points[i]);
      assert.deepEqual(curve.end, points[i + 1]);
      let prior = curve.start.y;
      for (let step = 0; step <= 100; step++) {
        const y = bezier(curve.start.y, curve.first.y, curve.second.y, curve.end.y, step / 100);
        assert.ok(y >= Math.min(curve.start.y, curve.end.y) - 1e-9 && y <= Math.max(curve.start.y, curve.end.y) + 1e-9);
        if (curve.end.y >= curve.start.y) assert.ok(y >= prior - 1e-9); else assert.ok(y <= prior + 1e-9);
        prior = y;
      }
    });
  }
});

test('curve paths reject duplicate x positions and invalid coordinates', () => {
  assert.equal(curvePath([]), '');
  assert.equal(curvePath([{ x: 0, y: 1 }]), '');
  assert.equal(curvePath([{ x: 0, y: 1 }, { x: 0, y: 2 }]), '');
  assert.equal(curvePath([{ x: 0, y: NaN }, { x: 1, y: 2 }]), '');
  assert.match(curvePath([{ x: 0, y: 0 }, { x: 1, y: 2 }]), /^M0\.000,0\.000 C/);
});

test('server segment boundaries survive downsampling and no new gap is inferred between representative samples', () => {
  const parts = chartSegments([[observation(0, 1), observation(60, 2)], [observation(90, 3), observation(120, 4)]]);
  assert.deepEqual(parts.map(part => part.map(point => point.value)), [[1, 2], [3, 4]]);
});

test('invalid observations and reset counters split curves instead of joining across them', () => {
  const invalid = { ...observation(5, 10), collected_at: 'invalid' };
  const parts = chartSegments([[observation(0, 1), invalid, observation(10, 5), observation(15, 2), observation(20, 3)]]);
  assert.deepEqual(parts.map(part => part.map(point => point.value)), [[1], [5], [2, 3]]);
});

test('hover shows original values and leaves unavailable time gaps unselected', () => {
  const parts = chartSegments([[observation(0, 10), observation(5, 12)], [observation(20, 20), observation(25, 22)]]);
  const time = (minute: number) => Date.parse(observation(minute, 0).collected_at);
  assert.equal(hoverPoint(parts, time(2), 0)?.value, 10);
  assert.equal(hoverPoint(parts, time(4), 0)?.value, 12);
  assert.equal(hoverPoint(parts, time(10), 0), null);
  assert.equal(hoverPoint(parts, time(20), 0)?.value, 20);
  assert.equal(hoverPoint(parts, time(26), 0), null);
});

test('an isolated observation can be selected without filling the surrounding gap', () => {
  const parts = chartSegments([[observation(5, 0)]]);
  const time = parts[0][0].time;
  assert.equal(hoverPoint(parts, time + 500, 1000)?.value, 0);
  assert.equal(hoverPoint(parts, time + 2000, 1000), null);
});

function details(): AccountDetails {
  return {
    account: { id: '1', account: 'sample@example.com', plan: 'claude/cc-max-20x', vendor_id: '1', vendor_name: 'TK Register 0', current_submission_id: '1', latest_submission_id: '1', submission_status: 'accepted', remote_id: '1', platform_account_id: '1', last_success_at: null, last_attempt_at: null, last_error: '', snapshot: null },
    snapshots: [], snapshot_count: 5, snapshot_page: 1, snapshot_page_size: 50,
    trend: { cost_total_usd: [[observation(0, 0), observation(5, 5)], [observation(20, 10), observation(25, 12)]], input_tokens_total: [], output_tokens_total: [] },
    trend_reduced: true, interval_seconds: 300, timezone: 'Asia/Shanghai',
  };
}

test('rendered chart draws separate smooth paths without permanent dots and provides keyboard access', () => {
  const html = renderToStaticMarkup(createElement(UsageTrend, { data: details(), endpoint: '/accounts/1/snapshots', days: '7', metric: 'cost_total_usd' }));
  assert.equal((html.match(/<path /g) || []).length, 2);
  assert.doesNotMatch(html, /<polyline|<circle/);
  assert.match(html, /tabindex="0"/);
  assert.match(html, /left and right arrow keys/);
  assert.match(html, /representative observations/);
  assert.match(html, /View data/);
});

test('empty metrics remain unavailable instead of drawing a zero-value curve', () => {
  const html = renderToStaticMarkup(createElement(UsageTrend, { data: details(), endpoint: '/accounts/1/snapshots', days: '7', metric: 'input_tokens_total' }));
  assert.match(html, /No usage data yet/);
  assert.doesNotMatch(html, /<svg/);
});
