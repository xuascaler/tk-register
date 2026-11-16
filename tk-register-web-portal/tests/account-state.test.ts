import assert from 'node:assert/strict';
import test from 'node:test';
import { deletionInProgress } from '../src/accounts/account-state.ts';
import { statusLabel, statusTone } from '../src/accounts/helpers.ts';

test('deleted and pending accounts block account actions while failed operations release the guard', () => {
  assert.equal(deletionInProgress({}), false);
  assert.equal(deletionInProgress({ deletion_status: '' }), false);
  assert.equal(deletionInProgress({ deletion_status: 'failed' }), false);
  for (const deletion_status of ['pending', 'awaiting_verification', 'upstream_confirmed', 'completed']) assert.equal(deletionInProgress({ deletion_status }), true);
  assert.equal(deletionInProgress({ deleted_at: '2026-11-04T00:00:00Z' }), true);
});

test('upstream sync_failed remains distinct from unknown and is shown as a failure', () => {
  assert.equal(statusLabel('sync_failed'), 'Sync error');
  assert.equal(statusTone('sync_failed'), 'negative');
  assert.equal(statusLabel('unknown'), 'Unknown');
  assert.equal(statusTone('unknown'), 'neutral');
});
