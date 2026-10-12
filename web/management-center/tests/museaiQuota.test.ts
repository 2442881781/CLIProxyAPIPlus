import { describe, expect, test } from 'bun:test';
import type { TFunction } from 'i18next';
import { parseMuseaiQuota } from '../src/features/quota/providers/museai/data';
import { buildTimelineLane } from '../src/features/quota/quotaTimelineModel';
import { nextRecoveryMs } from '../src/features/quota/resetSchedule';

const t = ((key: string) => key) as unknown as TFunction;
const resetTime = '2026-10-14T09:39:41Z';

describe('Muse native account quota', () => {
  test('preserves remaining percentages, site token units, and the actual reset', () => {
    const data = parseMuseaiQuota(
      {
        subscription: { plan: 'Free plan', tierName: 'Muse Free' },
        groups: [
          {
            buckets: [
              { window: 'weekly', remainingFraction: 0.96, resetTime },
              {
                window: 'additional_tokens',
                remainingFraction: 1,
                description: '0% used (9B tokens left)',
              },
            ],
          },
        ],
      },
      t
    );
    expect(data.plan).toBe('Free plan');
    expect(data.windows[0].usedPercent).toBeCloseTo(4, 8);
    expect(data.windows[0].remainingPercent).toBe(96);
    expect(data.windows[1].description).toBe('0% used (9B tokens left)');
    expect(data.windows[1].resetAtMs).toBeNull();
    expect(data.balance).toBeUndefined();
    const quota = { status: 'success' as const, ...data };
    expect(nextRecoveryMs('museai', quota, Date.parse('2026-10-12T00:00:00Z'))).toBe(
      Date.parse(resetTime)
    );
    const lane = buildTimelineLane({
      name: 'account',
      displayName: 'Muse',
      provider: 'museai',
      quota,
    });
    expect(lane.anchorMs).toBe(Date.parse(resetTime));
    expect(lane.periodHours).toBe(168);
    expect(lane.remaining).toBe(96);
  });

  test('does not present malformed or missing fractions as exhausted quota', () => {
    for (const remainingFraction of [undefined, null, '', -1, 1.01, NaN, Infinity]) {
      expect(() =>
        parseMuseaiQuota({ groups: [{ buckets: [{ window: 'weekly', remainingFraction }] }] }, t)
      ).toThrow('provider_quota.empty_data');
    }
    const exhausted = parseMuseaiQuota(
      { groups: [{ buckets: [{ window: 'weekly', remaining_fraction: 0 }] }] },
      t
    );
    expect(exhausted.windows[0].remainingPercent).toBe(0);
    expect(exhausted.windows[0].resetAtMs).toBeNull();
  });

  test('distinguishes confirmed unlimited usage from an empty response', () => {
    expect(parseMuseaiQuota({ unlimited: true, subscription: { plan: 'Unlimited' } }, t)).toEqual({
      windows: [],
      plan: 'Unlimited',
      unlimited: true,
    });
    expect(() => parseMuseaiQuota({ subscription: { plan: 'Unknown' } }, t)).toThrow(
      'provider_quota.empty_data'
    );
  });
});
