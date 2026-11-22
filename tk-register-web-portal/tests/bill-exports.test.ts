import assert from 'node:assert/strict';
import test from 'node:test';
import { BILL_EXPORT_USER_LIMIT, billExportUsers, sameBillExportUsers, toggleBillExportUser, beijingDate, billAmount, billSubmittedTime, exportDateError, offsetDate } from '../src/admin/bill-exports.ts';

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

test('bill export selections retain unique user IDs and stop at twenty without preventing removal', () => {
  const selected = Array.from({ length: BILL_EXPORT_USER_LIMIT }, (_, index) => ({ id: String(index + 1), email: `user${index + 1}@example.test` }));
  const extra = { id: '21', email: 'extra@example.test' };
  assert.deepEqual(toggleBillExportUser(selected, extra), selected);
  const removed = toggleBillExportUser(selected, { ...selected[0], email: 'renamed@example.test' });
  assert.equal(removed.length, 19);
  assert.ok(!removed.some(user => user.id === '1'));
  assert.equal(toggleBillExportUser(removed, extra).length, 20);
  assert.deepEqual(toggleBillExportUser([selected[0], selected[0]], extra), [selected[0], extra]);
  assert.equal(selected.length, 20, 'selection updates do not mutate saved references');
});

test('bill export users use the task snapshot and fall back to the old single-user response', () => {
  const first = { id: '1', email: 'first@example.test' };
  const second = { id: '2', email: 'second@example.test' };
  assert.deepEqual(billExportUsers({ users: [first, second], user: first }), [first, second]);
  assert.deepEqual(billExportUsers({ user: first }), [first]);
  assert.deepEqual(billExportUsers({ users: [], user: first }), [first]);
  assert.deepEqual(billExportUsers({ users: [second, second] }), [second]);
  assert.deepEqual(billExportUsers(null), []);
});

test('bill export selection changes compare identity independently of order and email updates', () => {
  const first = { id: '1', email: 'first@example.test' };
  const second = { id: '2', email: 'second@example.test' };
  assert.equal(sameBillExportUsers([first, second], [second, first]), true);
  assert.equal(sameBillExportUsers([first], [{ ...first, email: 'updated@example.test' }]), true);
  assert.equal(sameBillExportUsers([first], [{ id: '3', email: first.email }]), false);
  assert.equal(sameBillExportUsers([first, second], [first]), false);
  assert.equal(sameBillExportUsers([], []), true);
});
