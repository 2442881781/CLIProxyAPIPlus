import type { ModelPressureRow, ModelPressureSnapshot } from '@/types';
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

const normalizeModelPressureRow = (value: unknown): ModelPressureRow => {
  const row = asRecord(value);
  return {
    model: asString(row.model) || 'unknown',
    inFlight: asNumber(row.in_flight ?? row.inFlight),
    requestsPerSecond: asNumber(row.requests_per_second ?? row.requestsPerSecond),
    inputTokensPerSecond: asNumber(row.input_tokens_per_second ?? row.inputTokensPerSecond),
    outputTokensPerSecond: asNumber(row.output_tokens_per_second ?? row.outputTokensPerSecond),
    totalTokensPerSecond: asNumber(row.total_tokens_per_second ?? row.totalTokensPerSecond),
    failedPerSecond: asNumber(row.failed_per_second ?? row.failedPerSecond),
    errorRate: asNumber(row.error_rate ?? row.errorRate),
    avgLatencyMs: asNumber(row.avg_latency_ms ?? row.avgLatencyMs),
    avgTtftMs: asNumber(row.avg_ttft_ms ?? row.avgTtftMs),
    servingAuths: asNumber(row.serving_auths ?? row.servingAuths),
    suspendedAuths: asNumber(row.suspended_auths ?? row.suspendedAuths),
    quotaExceededAuths: asNumber(row.quota_exceeded_auths ?? row.quotaExceededAuths),
    providers: Array.isArray(row.providers) ? row.providers.map(asString).filter(Boolean) : [],
  };
};

export const normalizeModelPressureSnapshot = (value: unknown): ModelPressureSnapshot => {
  const snapshot = asRecord(value);
  return {
    windowSeconds: asNumber(snapshot.window_seconds ?? snapshot.windowSeconds) || 60,
    models: Array.isArray(snapshot.models) ? snapshot.models.map(normalizeModelPressureRow) : [],
  };
};

export const modelPressureApi = {
  getSnapshot: async (): Promise<ModelPressureSnapshot> =>
    normalizeModelPressureSnapshot(await apiClient.get('/model-pressure')),
};
