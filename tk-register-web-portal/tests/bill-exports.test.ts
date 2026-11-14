import assert from 'node:assert/strict';
import test from 'node:test';
import { beijingDate, billAmount, billSubmittedTime, exportDateError, offsetDate } from '../src/admin/bill-exports.ts';

test('bill export dates use the Beijing day even before UTC midnight', () => {
  assert.equal(beijingDate(new Date('2026-11-11T16:01:00Z')), '2026-11-12');
  assert.equal(beijingDate(new Date('2026-11-11T15:59:00Z')), '2026-11-11');
});

test('bill export accepts exactly sixty inclusive days and rejects sixty-one', () => {
  assert.equal(exportDateError('2026-09-14', '2026-11-12', '2026-11-12'), '');
  assert.match(exportDateError('2026-09-13', '2026-11-12', '2026-11-12'), /60 days/);
  assert.equal(exportDateError('2026-11-12', '2026-11-12', '2026-11-12'), '');
});

test('bill export rejects invalid, reversed, or future ranges', () => {
  for (const date of ['', '2026-02-29', '2026-04-31', '2026-13-01', 'not-a-date']) {
    assert.match(exportDateError(date, '2026-11-12', '2026-11-12'), /valid/);
  }
  assert.match(exportDateError('2026-11-12', '2026-11-11', '2026-11-12'), /on or after/);
  assert.match(exportDateError('2026-11-12', '2026-11-13', '2026-11-12'), /future/);
  assert.equal(offsetDate('2028-03-01', -1), '2028-02-29');
});

test('missing bill amounts are not displayed as zero', () => {
  assert.equal(billAmount(null), '—');
  assert.equal(billAmount(undefined), '—');
  assert.equal(billAmount(''), '—');
  assert.equal(billAmount('0'), '0.00');
  assert.equal(billAmount('14076.57'), '14,076.57');
  assert.equal(billAmount('not-a-number'), '—');
});

test('bill submission times use Beijing time and preserve missing values in old exports', () => {
  assert.equal(billSubmittedTime('2026-11-13T16:00:05Z'), '2026-11-14 00:00:05');
  assert.equal(billSubmittedTime('2026-11-13T23:15:29-07:00'), '2026-11-14 14:15:29');
  for (const value of [null, undefined, '', 'bad', '0001-01-01T00:00:00Z']) {
    assert.equal(billSubmittedTime(value), '—');
  }
});
