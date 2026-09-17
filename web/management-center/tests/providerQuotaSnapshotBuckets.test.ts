import { afterEach, describe, expect, test, spyOn } from 'bun:test';
import type { TFunction } from 'i18next';
import { OPENCODE_CONFIG } from '../src/features/quota/providers/providerApi/data';
import {
  providerQuotasApi,
  type ProviderQuotaBucketSnapshot,
  type ProviderQuotaSnapshot,
} from '../src/services/api';
import type { AuthFileItem } from '../src/types';

// Feature: server-backed quota buckets must be read in the server's own casing
//
//   The provider quota API serializes normalized plugin buckets in camelCase
//   (`remainingFraction`). The card read only `remaining_fraction`, so every
//   fraction resolved to undefined and the fallback turned it into "0%
//   remaining" — an exhausted-looking card for a pool with plenty of quota.
//   These tests pin the bucket parsing for the CommandCode/OpenCode path.

const t = ((key: string) => key) as unknown as TFunction;
const file = { name: 'OpenCode Go #1 · ••••YQQC', type: 'opencode' } as unknown as AuthFileItem;

const spies: Array<{ mockRestore: () => void }> = [];
afterEach(() => {
  while (spies.length > 0) {
    spies.pop()?.mockRestore();
  }
});

const snapshotWith = (buckets: ProviderQuotaBucketSnapshot[]): ProviderQuotaSnapshot => ({
  provider: 'opencode-go',
  remaining_percent: 91,
  observed_at: '2026-09-17T14:39:51Z',
  sources: [
    {
      id: 'opencode-go-source-1',
      label: 'OpenCode Go #1',
      remaining_percent: 91,
      observed_at: '2026-09-17T14:39:51Z',
      last_attempt_at: '2026-09-17T14:39:51Z',
      quota: { groups: [{ displayName: 'OpenCode Go', buckets }] },
    },
  ],
});

describe('server-backed quota bucket casing', () => {
  test('reads camelCase buckets from the server as remaining quota', async () => {
    spies.push(
      spyOn(providerQuotasApi, 'refresh').mockResolvedValue(
        snapshotWith([
          { window: 'rolling', remainingFraction: 1, resetTime: '2026-09-17T18:22:25.073Z' },
          { window: 'weekly', remainingFraction: 0.91, resetTime: '2026-09-21T00:00:00.000Z' },
          { window: 'monthly', remainingFraction: 0.96, resetTime: '2026-10-16T12:32:18.000Z' },
        ])
      )
    );

    const quota = await OPENCODE_CONFIG.fetchQuota(file, t);
    const byId = new Map(quota.windows.map((window) => [window.id, window]));

    expect([...byId.keys()]).toEqual(['rolling', 'weekly', 'monthly']);
    expect(byId.get('rolling')?.remainingPercent).toBeCloseTo(100, 5);
    expect(byId.get('rolling')?.usedPercent).toBeCloseTo(0, 5);
    expect(byId.get('weekly')?.remainingPercent).toBeCloseTo(91, 5);
    expect(byId.get('weekly')?.usedPercent).toBeCloseTo(9, 5);
    expect(byId.get('monthly')?.remainingPercent).toBeCloseTo(96, 5);
    expect(byId.get('weekly')?.resetAtMs).toBe(Date.parse('2026-09-21T00:00:00.000Z'));
  });

  test('still reads snake_case buckets from older snapshots', async () => {
    spies.push(
      spyOn(providerQuotasApi, 'refresh').mockResolvedValue(
        snapshotWith([
          { window: 'weekly', remaining_fraction: 0.5, reset_time: '2026-09-21T00:00:00.000Z' },
        ])
      )
    );

    const quota = await OPENCODE_CONFIG.fetchQuota(file, t);
    expect(quota.windows).toHaveLength(1);
    expect(quota.windows[0]?.remainingPercent).toBeCloseTo(50, 5);
    expect(quota.windows[0]?.usedPercent).toBeCloseTo(50, 5);
  });

  test('skips a bucket without a usable fraction instead of reporting it as used', async () => {
    spies.push(
      spyOn(providerQuotasApi, 'refresh').mockResolvedValue(
        snapshotWith([{ window: 'rolling' }, { window: 'weekly', remainingFraction: 0.91 }])
      )
    );

    const quota = await OPENCODE_CONFIG.fetchQuota(file, t);
    expect(quota.windows.map((window) => window.id)).toEqual(['weekly']);
    expect(quota.windows[0]?.remainingPercent).toBeCloseTo(91, 5);
  });

  test('reports the server reason when no bucket carries a fraction', async () => {
    spies.push(
      spyOn(providerQuotasApi, 'refresh').mockResolvedValue(snapshotWith([{ window: 'rolling' }]))
    );

    await expect(OPENCODE_CONFIG.fetchQuota(file, t)).rejects.toThrow('provider_quota.empty_data');
  });
});
