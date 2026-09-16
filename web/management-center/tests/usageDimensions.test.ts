import { describe, expect, test } from 'bun:test';
import { normalizeAccessKeyUsageDetail } from '../src/services/api/accessControl';

// Feature: usage-monitor dimension tabs
//
//   The monitor page gains tabs: overview (existing), per-key leaderboard,
//   per-group rollup. Selecting a row loads its dimension detail
//   (models/daily/auths) for drill-down.

describe('usage dimension api normalization', () => {
  test('normalizes /access-keys/usage detail (summary + models + daily + auths)', () => {
    const detail = normalizeAccessKeyUsageDetail({
      id: 'key-1',
      name: 'Alice',
      key_prefix: 'sk-cpa-abc123',
      group: 'infra',
      usage: {
        total_tokens: 9999,
        period_tokens: 500,
        requests: 120,
        failed: 3,
        last_used_at: '2026-09-16T03:00:00Z',
      },
      models: [{ model: 'gpt-5', tokens: 6000, requests: 80, failed: 1 }],
      daily: [{ day: '2026-09-16', tokens: 500, requests: 12 }],
      auths: [{ auth: 'codex-a', tokens: 9999, requests: 120, failed: 3 }],
    });
    expect(detail).toMatchObject({
      id: 'key-1',
      keyPrefix: 'sk-cpa-abc123',
      group: 'infra',
      totalTokens: 9999,
      periodTokens: 500,
      requests: 120,
      failed: 3,
    });
    expect(detail.models[0]).toMatchObject({ name: 'gpt-5', tokens: 6000 });
    expect(detail.daily[0]).toMatchObject({ name: '2026-09-16', tokens: 500 });
    expect(detail.auths[0]).toMatchObject({ name: 'codex-a', failed: 3 });
  });

  test('handles empty detail payloads without throwing', () => {
    const detail = normalizeAccessKeyUsageDetail({});
    expect(detail.models).toEqual([]);
    expect(detail.daily).toEqual([]);
    expect(detail.auths).toEqual([]);
    expect(detail.totalTokens).toBe(0);
  });
});
