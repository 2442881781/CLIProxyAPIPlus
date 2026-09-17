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

  test('normalizes byte traffic totals and per-dimension bytes', () => {
    // Feature: per-key traffic cost view. The operator-only summary exposes
    // both legs, and every dimension row carries its byte counters.
    const detail = normalizeAccessKeyUsageDetail({
      id: 'key-bytes',
      name: 'Traffic',
      key_prefix: 'sk-cpa-traffic',
      group: '',
      usage: {
        total_tokens: 10,
        requests: 1,
        bytes: {
          client_in: 100,
          client_out: 400,
          upstream_in: 900,
          upstream_out: 200,
          client_total: 500,
          upstream_total: 1100,
          total: 1600,
          period_total: 1600,
        },
      },
      models: [{ model: 'gpt-5', tokens: 10, requests: 1, bytes_in: 1000, bytes_out: 600 }],
      daily: [{ day: '2026-09-17', tokens: 10, requests: 1, bytes_in: 1000, bytes_out: 600 }],
      auths: [{ auth: 'codex-a', tokens: 10, requests: 1, bytes_in: 900, bytes_out: 200 }],
    });
    expect(detail.traffic).toEqual({
      clientIn: 100,
      clientOut: 400,
      upstreamIn: 900,
      upstreamOut: 200,
      clientTotal: 500,
      upstreamTotal: 1100,
      total: 1600,
      periodTotal: 1600,
    });
    expect(detail.models[0]).toMatchObject({ name: 'gpt-5', bytesIn: 1000, bytesOut: 600, bytes: 1600 });
    expect(detail.daily[0]).toMatchObject({ name: '2026-09-17', bytes: 1600 });
    expect(detail.auths[0]).toMatchObject({ name: 'codex-a', bytesIn: 900, bytesOut: 200 });
  });

  test('leaves traffic undefined for legacy payloads without byte counters', () => {
    const detail = normalizeAccessKeyUsageDetail({ usage: { total_tokens: 5 } });
    expect(detail.traffic).toBeUndefined();
    expect(detail.models).toEqual([]);
  });
});
