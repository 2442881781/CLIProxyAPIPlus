import type {
  UsageMonitorModelStats,
  UsageMonitorProviderStats,
  UsageMonitorSnapshot,
  UsageMonitorTokenTotals,
} from '@/types';
import { apiClient } from './client';

const asRecord = (value: unknown): Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};

const asNumber = (value: unknown): number => {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? number : 0;
};

const asString = (value: unknown): string => (typeof value === 'string' ? value : '');

const normalizeTokens = (value: unknown): UsageMonitorTokenTotals => {
  const tokens = asRecord(value);
  return {
    inputTokens: asNumber(tokens.input_tokens ?? tokens.inputTokens),
    outputTokens: asNumber(tokens.output_tokens ?? tokens.outputTokens),
    reasoningTokens: asNumber(tokens.reasoning_tokens ?? tokens.reasoningTokens),
    cacheReadTokens: asNumber(tokens.cache_read_tokens ?? tokens.cacheReadTokens),
    cacheCreationTokens: asNumber(tokens.cache_creation_tokens ?? tokens.cacheCreationTokens),
    unclassifiedTokens: asNumber(tokens.unclassified_tokens ?? tokens.unclassifiedTokens),
    totalTokens: asNumber(tokens.total_tokens ?? tokens.totalTokens),
  };
};

const normalizeModel = (value: unknown): UsageMonitorModelStats => {
  const model = asRecord(value);
  return {
    model: asString(model.model) || 'unknown',
    requests: asNumber(model.requests),
    success: asNumber(model.success),
    failed: asNumber(model.failed),
    averageLatencyMs: asNumber(model.average_latency_ms ?? model.averageLatencyMs),
    lastUsedAt: asString(model.last_used_at ?? model.lastUsedAt),
    tokens: normalizeTokens(model.tokens),
  };
};

const normalizeProvider = (value: unknown): UsageMonitorProviderStats => {
  const provider = asRecord(value);
  return {
    provider: asString(provider.provider) || 'unknown',
    requests: asNumber(provider.requests),
    success: asNumber(provider.success),
    failed: asNumber(provider.failed),
    averageLatencyMs: asNumber(provider.average_latency_ms ?? provider.averageLatencyMs),
    lastUsedAt: asString(provider.last_used_at ?? provider.lastUsedAt),
    tokens: normalizeTokens(provider.tokens),
    models: Array.isArray(provider.models) ? provider.models.map(normalizeModel) : [],
  };
};

export const normalizeUsageMonitorSnapshot = (value: unknown): UsageMonitorSnapshot => {
  const snapshot = asRecord(value);
  return {
    enabled: snapshot.enabled === true,
    since: asString(snapshot.since),
    updatedAt: asString(snapshot.updated_at ?? snapshot.updatedAt) || undefined,
    truncated: snapshot.truncated === true,
    requests: asNumber(snapshot.requests),
    success: asNumber(snapshot.success),
    failed: asNumber(snapshot.failed),
    tokens: normalizeTokens(snapshot.tokens),
    providers: Array.isArray(snapshot.providers) ? snapshot.providers.map(normalizeProvider) : [],
  };
};

export const usageMonitorApi = {
  getSnapshot: async (): Promise<UsageMonitorSnapshot> =>
    normalizeUsageMonitorSnapshot(await apiClient.get('/usage-monitor')),
};
