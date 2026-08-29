import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import test from 'node:test';

const root = path.resolve(import.meta.dirname, '../../../../../');
const read = (relativePath) => fs.readFileSync(path.join(root, relativePath), 'utf8');

test('provider quota data preserves subscription snapshots', () => {
  const quotas = read('frontend/src/features/system/data/quotas.ts');
  assert.match(quotas, /_subscriptions/);
  assert.match(quotas, /parseChannelNodeBase/);
  assert.match(quotas, /subscriptions\??:/);
});

test('provider quota popover renders expandable subscription rows', () => {
  const badges = read('frontend/src/components/quota-badges.tsx');
  assert.match(badges, /subscriptionsExpanded/);
  assert.match(badges, /aria-expanded=\{subscriptionsExpanded\}/);
  assert.match(badges, /channel\.subscriptions\.map/);
  assert.match(badges, /isSubscription/);
});
