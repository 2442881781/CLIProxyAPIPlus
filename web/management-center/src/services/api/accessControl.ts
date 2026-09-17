import type {
  AccessAuthItem,
  AccessGroup,
  AccessGroupUsageDetail,
  AccessKeyUsageDetail,
  AccessKeyUsagePeriod,
  AccessKeyUsageTopBy,
  AccessKeyUsageTopRow,
  AccessTrafficTotals,
  RateLimitSpec,
  UsageDimRow,
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

const asStringList = (value: unknown): string[] =>
  Array.isArray(value) ? value.map(asString).filter(Boolean) : [];

const normalizeRateLimit = (value: unknown): RateLimitSpec => {
  const raw = asRecord(value);
  return {
    rpm: asNumber(raw.rpm),
    tpm: asNumber(raw.tpm),
    rpd: asNumber(raw.rpd),
    maxConcurrency: asNumber(raw.max_concurrency ?? raw.maxConcurrency),
  };
};

const normalizeUsageTotals = (value: unknown): AccessGroup['usage'] => {
  const raw = asRecord(value);
  // Group list rows carry the full Usage shape (client_/upstream_ legs summed
  // on the server); group detail totals carry the DimUsage shape
  // (in_bytes/out_bytes). The shapes never mix, so summing is safe.
  const bytesIn =
    asNumber(raw.in_bytes ?? raw.bytes_in) +
    asNumber(raw.client_in_bytes) +
    asNumber(raw.upstream_in_bytes);
  const bytesOut =
    asNumber(raw.out_bytes ?? raw.bytes_out) +
    asNumber(raw.client_out_bytes) +
    asNumber(raw.upstream_out_bytes);
  return {
    tokens: asNumber(raw.tokens ?? raw.total_tokens),
    requests: asNumber(raw.requests),
    failed: asNumber(raw.failed),
    lastUsedAt: asString(raw.last_used_at ?? raw.lastUsedAt),
    bytesIn,
    bytesOut,
    bytes: bytesIn + bytesOut,
  };
};

export const normalizeAccessGroup = (value: unknown): AccessGroup => {
  const raw = asRecord(value);
  return {
    name: asString(raw.name),
    allowedAuths: asStringList(raw.allowed_auths ?? raw.allowedAuths),
    allowedModels: asStringList(raw.allowed_models ?? raw.allowedModels),
    maxConcurrency: asNumber(raw.max_concurrency ?? raw.maxConcurrency),
    rateLimitRpm: asNumber(raw.rate_limit_rpm ?? raw.rateLimitRpm),
    perKeyLimits: normalizeRateLimit(raw.per_key_limits ?? raw.perKeyLimits),
    usage: normalizeUsageTotals(raw.usage),
    createdAt: asString(raw.created_at ?? raw.createdAt),
    updatedAt: asString(raw.updated_at ?? raw.updatedAt),
  };
};

export interface AccessGroupPayload {
  name: string;
  allowedAuths: string[];
  allowedModels: string[];
  maxConcurrency: number;
  rateLimitRpm: number;
  perKeyLimits: RateLimitSpec;
}

const serializeRateLimit = (limits: RateLimitSpec): Record<string, number> | undefined => {
  const out: Record<string, number> = {};
  if (limits.rpm !== 0) out.rpm = limits.rpm;
  if (limits.tpm !== 0) out.tpm = limits.tpm;
  if (limits.rpd !== 0) out.rpd = limits.rpd;
  if (limits.maxConcurrency !== 0) out.max_concurrency = limits.maxConcurrency;
  return Object.keys(out).length ? out : undefined;
};

export const serializeAccessGroup = (payload: AccessGroupPayload): Record<string, unknown> => {
  const body: Record<string, unknown> = { name: payload.name.trim() };
  if (payload.allowedAuths.length) body.allowed_auths = payload.allowedAuths;
  if (payload.allowedModels.length) body.allowed_models = payload.allowedModels;
  if (payload.maxConcurrency > 0) body.max_concurrency = payload.maxConcurrency;
  if (payload.rateLimitRpm > 0) body.rate_limit_rpm = payload.rateLimitRpm;
  const limits = serializeRateLimit(payload.perKeyLimits);
  if (limits) body.per_key_limits = limits;
  return body;
};

const normalizeDimRow = (value: unknown, keyField: string): UsageDimRow => {
  const raw = asRecord(value);
  const bytesIn = asNumber(raw.bytes_in ?? raw.bytesIn);
  const bytesOut = asNumber(raw.bytes_out ?? raw.bytesOut);
  return {
    name: asString(raw[keyField]),
    tokens: asNumber(raw.tokens),
    requests: asNumber(raw.requests),
    failed: asNumber(raw.failed),
    lastUsedAt: asString(raw.last_used_at ?? raw.lastUsedAt),
    bytesIn,
    bytesOut,
    bytes: bytesIn + bytesOut,
  };
};

const normalizeTraffic = (value: unknown): AccessTrafficTotals | undefined => {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined;
  const raw = asRecord(value);
  return {
    clientIn: asNumber(raw.client_in),
    clientOut: asNumber(raw.client_out),
    upstreamIn: asNumber(raw.upstream_in),
    upstreamOut: asNumber(raw.upstream_out),
    clientTotal: asNumber(raw.client_total),
    upstreamTotal: asNumber(raw.upstream_total),
    total: asNumber(raw.total),
    periodTotal: asNumber(raw.period_total),
  };
};

const normalizeDimRows = (value: unknown, keyField: string): UsageDimRow[] =>
  Array.isArray(value) ? value.map((row) => normalizeDimRow(row, keyField)) : [];

export const normalizeAccessGroupUsage = (value: unknown): AccessGroupUsageDetail => {
  const raw = asRecord(value);
  return {
    name: asString(raw.name),
    keys: asNumber(raw.keys),
    totals: normalizeUsageTotals(raw.totals),
    avgLatencyMs: asNumber(raw.avg_latency_ms ?? raw.avgLatencyMs),
    avgTtftMs: asNumber(raw.avg_ttft_ms ?? raw.avgTtftMs),
    models: normalizeDimRows(raw.models, 'model'),
    daily: normalizeDimRows(raw.daily, 'day'),
    auths: normalizeDimRows(raw.auths, 'auth'),
  };
};

const normalizeUsageTopRow = (value: unknown): AccessKeyUsageTopRow => {
  const raw = asRecord(value);
  const bytesIn = asNumber(raw.bytes_in);
  const bytesOut = asNumber(raw.bytes_out);
  return {
    id: asString(raw.id),
    name: asString(raw.name),
    keyPrefix: asString(raw.key_prefix ?? raw.keyPrefix),
    group: asString(raw.group),
    tokens: asNumber(raw.tokens),
    requests: asNumber(raw.requests),
    failed: asNumber(raw.failed),
    lastUsedAt: asString(raw.last_used_at ?? raw.lastUsedAt),
    bytesIn,
    bytesOut,
    bytes: bytesIn + bytesOut,
  };
};

export const normalizeAccessKeyUsageDetail = (value: unknown): AccessKeyUsageDetail => {
  const raw = asRecord(value);
  const usage = asRecord(raw.usage);
  return {
    id: asString(raw.id),
    name: asString(raw.name),
    keyPrefix: asString(raw.key_prefix ?? raw.keyPrefix),
    group: asString(raw.group),
    totalTokens: asNumber(usage.total_tokens ?? usage.totalTokens),
    periodTokens: asNumber(usage.period_tokens ?? usage.periodTokens),
    requests: asNumber(usage.requests),
    failed: asNumber(usage.failed),
    lastUsedAt: asString(usage.last_used_at ?? usage.lastUsedAt),
    models: normalizeDimRows(raw.models, 'model'),
    daily: normalizeDimRows(raw.daily, 'day'),
    auths: normalizeDimRows(raw.auths, 'auth'),
    traffic: normalizeTraffic(usage.bytes),
  };
};

export const accessControlApi = {
  listGroups: async (): Promise<AccessGroup[]> => {
    const raw = await apiClient.get('/access-groups');
    const groups = asRecord(raw).groups;
    return Array.isArray(groups) ? groups.map(normalizeAccessGroup) : [];
  },

  putGroup: (payload: AccessGroupPayload): Promise<unknown> =>
    apiClient.put('/access-groups', serializeAccessGroup(payload)),

  deleteGroup: (name: string): Promise<unknown> =>
    apiClient.delete(`/access-groups?name=${encodeURIComponent(name)}`),

  getGroupUsage: async (name: string): Promise<AccessGroupUsageDetail> =>
    normalizeAccessGroupUsage(
      await apiClient.get(`/access-groups/usage?name=${encodeURIComponent(name)}`)
    ),

  listAuths: async (): Promise<AccessAuthItem[]> => {
    const raw = await apiClient.get('/access-auths');
    const auths = asRecord(raw).auths;
    if (!Array.isArray(auths)) return [];
    return auths.map((item) => {
      const raw = asRecord(item);
      return {
        id: asString(raw.id),
        label: asString(raw.label),
        provider: asString(raw.provider),
        prefix: asString(raw.prefix),
        file: asString(raw.file),
        disabled: raw.disabled === true,
      };
    });
  },

  getKeyUsageTop: async (
    by: AccessKeyUsageTopBy,
    period: AccessKeyUsagePeriod,
    limit = 20
  ): Promise<AccessKeyUsageTopRow[]> => {
    const raw = await apiClient.get(
      `/access-keys/usage-top?by=${by}&period=${period}&limit=${limit}`
    );
    const keys = asRecord(raw).keys;
    return Array.isArray(keys) ? keys.map(normalizeUsageTopRow) : [];
  },

  getKeyUsageDetail: async (id: string): Promise<AccessKeyUsageDetail> =>
    normalizeAccessKeyUsageDetail(
      await apiClient.get(`/access-keys/usage?id=${encodeURIComponent(id)}`)
    ),
};
