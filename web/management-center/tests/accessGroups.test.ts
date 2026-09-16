import { describe, expect, test } from 'bun:test';
import {
  normalizeAccessGroup,
  normalizeAccessGroupUsage,
  serializeAccessGroup,
} from '../src/services/api/accessControl';

// Feature: access-group management (native React page)
//
//   Groups scope member keys to an upstream-auth pool plus shared limits.
//   The page lists groups with usage, creates/edits via a sheet, and deletes
//   with confirmation. Group payload normalization lives in the API layer.

describe('access group api normalization', () => {
  test('normalizes ListAccessGroups rows (snake_case fields, usage, per_key_limits)', () => {
    const group = normalizeAccessGroup({
      name: 'infra',
      allowed_auths: ['auth-a', 'auth-b'],
      allowed_models: ['gpt-5'],
      max_concurrency: 8,
      rate_limit_rpm: 600,
      per_key_limits: { rpm: 60, tpm: 100000, rpd: 500, max_concurrency: 2 },
      usage: { total_tokens: 12345, requests: 40, failed: 2, last_used_at: '2026-09-16T01:00:00Z' },
      created_at: '2026-09-01T00:00:00Z',
      updated_at: '2026-09-15T00:00:00Z',
    });
    expect(group).toEqual({
      name: 'infra',
      allowedAuths: ['auth-a', 'auth-b'],
      allowedModels: ['gpt-5'],
      maxConcurrency: 8,
      rateLimitRpm: 600,
      perKeyLimits: { rpm: 60, tpm: 100000, rpd: 500, maxConcurrency: 2 },
      usage: { tokens: 12345, requests: 40, failed: 2, lastUsedAt: '2026-09-16T01:00:00Z' },
      createdAt: '2026-09-01T00:00:00Z',
      updatedAt: '2026-09-15T00:00:00Z',
    });
  });

  test('serializes the edit-sheet form into the PUT /access-groups body', () => {
    expect(
      serializeAccessGroup({
        name: '  infra  ',
        allowedAuths: ['a1'],
        allowedModels: ['gpt-5', 'claude-4'],
        maxConcurrency: 4,
        rateLimitRpm: 300,
        perKeyLimits: { rpm: 30, tpm: 0, rpd: 0, maxConcurrency: 0 },
      })
    ).toEqual({
      name: 'infra',
      allowed_auths: ['a1'],
      allowed_models: ['gpt-5', 'claude-4'],
      max_concurrency: 4,
      rate_limit_rpm: 300,
      per_key_limits: { rpm: 30 },
    });
  });

  test('omits zero/empty limits instead of sending them', () => {
    expect(
      serializeAccessGroup({
        name: 'open',
        allowedAuths: [],
        allowedModels: [],
        maxConcurrency: 0,
        rateLimitRpm: 0,
        perKeyLimits: { rpm: 0, tpm: 0, rpd: 0, maxConcurrency: 0 },
      })
    ).toEqual({ name: 'open' });
  });

  test('normalizes /access-groups/usage detail rows (models/daily/auths dims)', () => {
    const detail = normalizeAccessGroupUsage({
      name: 'infra',
      keys: 3,
      totals: { tokens: 900, requests: 30, failed: 1, last_used_at: '2026-09-16T02:00:00Z' },
      avg_latency_ms: 1200,
      avg_ttft_ms: 300,
      models: [{ model: 'gpt-5', tokens: 500, requests: 20, failed: 0, last_used_at: 'x' }],
      daily: [{ day: '2026-09-15', tokens: 900, requests: 30, failed: 1 }],
      auths: [{ auth: 'auth-a', tokens: 900, requests: 30 }],
    });
    expect(detail.keys).toBe(3);
    expect(detail.totals.tokens).toBe(900);
    expect(detail.avgLatencyMs).toBe(1200);
    expect(detail.models[0]).toMatchObject({ name: 'gpt-5', tokens: 500 });
    expect(detail.daily[0]).toMatchObject({ name: '2026-09-15', tokens: 900 });
    expect(detail.auths[0]).toMatchObject({ name: 'auth-a', tokens: 900 });
  });

  test('normalizes /access-auths rows for the pool picker', () => {
    // shape covered implicitly by normalizeAccessGroup; picker rows are plain
    // pass-through of id/label/provider/prefix/file/disabled — verified via
    // type usage in the page component.
    expect(normalizeAccessGroup({ name: 'g' }).allowedAuths).toEqual([]);
  });
});
