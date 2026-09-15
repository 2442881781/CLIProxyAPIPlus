import { describe, expect, test } from 'bun:test';
import { normalizeUsageMonitorSnapshot } from '../src/services/api/usageMonitor';
import { filterUsageProviders, usageSuccessRate } from '../src/features/usageMonitor/logic';

describe('usage monitor API normalization', () => {
  test('normalizes provider and model token breakdowns from the backend contract', () => {
    const snapshot = normalizeUsageMonitorSnapshot({
      enabled: true,
      since: '2026-09-14T08:00:00Z',
      requests: 3,
      success: 2,
      failed: 1,
      tokens: {
        input_tokens: 120,
        output_tokens: 40,
        reasoning_tokens: 10,
        cache_read_tokens: 20,
        cache_creation_tokens: 5,
        total_tokens: 160,
      },
      providers: [
        {
          provider: 'commandcode',
          requests: 3,
          success: 2,
          failed: 1,
          average_latency_ms: 900,
          tokens: { total_tokens: 160 },
          models: [
            {
              model: 'glm-5',
              requests: 3,
              success: 2,
              failed: 1,
              last_used_at: '2026-09-14T08:03:00Z',
              tokens: { input_tokens: 120, output_tokens: 40, total_tokens: 160 },
            },
          ],
        },
      ],
    });

    expect(snapshot.tokens).toEqual({
      inputTokens: 120,
      outputTokens: 40,
      reasoningTokens: 10,
      cacheReadTokens: 20,
      cacheCreationTokens: 5,
      unclassifiedTokens: 0,
      totalTokens: 160,
    });
    expect(snapshot.providers[0]?.models[0]?.model).toBe('glm-5');
    expect(snapshot.providers[0]?.averageLatencyMs).toBe(900);
  });
});

describe('usage monitor filters', () => {
  const providers = normalizeUsageMonitorSnapshot({
    providers: [
      {
        provider: 'commandcode',
        models: [{ model: 'glm-5' }, { model: 'deepseek-v3' }],
      },
      {
        provider: 'codex',
        models: [{ model: 'gpt-5.4' }],
      },
    ],
  }).providers;

  test('filters whole providers by selection', () => {
    expect(filterUsageProviders(providers, 'codex', '')).toHaveLength(1);
    expect(filterUsageProviders(providers, 'codex', '')[0]?.provider).toBe('codex');
  });

  test('keeps only matching model rows for a model search', () => {
    const filtered = filterUsageProviders(providers, 'all', 'deepseek');
    expect(filtered).toHaveLength(1);
    expect(filtered[0]?.models.map((model) => model.model)).toEqual(['deepseek-v3']);
  });

  test('keeps every model when the provider name matches', () => {
    expect(filterUsageProviders(providers, 'all', 'command').at(0)?.models).toHaveLength(2);
  });

  test('computes success rates without producing NaN for an empty monitor', () => {
    expect(usageSuccessRate(8, 10)).toBe(80);
    expect(usageSuccessRate(0, 0)).toBeNull();
  });
});
