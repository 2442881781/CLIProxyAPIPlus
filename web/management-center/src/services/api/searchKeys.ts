/**
 * Web search provider key pool (Tavily / Exa / Firecrawl) management API.
 */

import { apiClient } from './client';

export const SEARCH_PROVIDERS = ['tavily', 'exa', 'firecrawl'] as const;
export type SearchProvider = (typeof SEARCH_PROVIDERS)[number];

export interface SearchKeyEntry {
  provider: string;
  apiKey: string;
  label: string;
  baseUrl: string;
  proxyUrl: string;
  disabled: boolean;
  /** Monthly USD budget tracked locally (Exa); 0 means none. */
  budget: number;
}

export interface SearchKeyQuota {
  /** 'credits' for Tavily/Firecrawl, 'usd' for Exa spend. */
  unit: string;
  used: number | null;
  limit: number | null;
  remaining: number | null;
  plan: string;
  resetAt: number;
  checkedAt: number;
  error: string;
}

export interface SearchKeyStatus {
  id: string;
  index: number;
  provider: string;
  maskedKey: string;
  /** Epoch milliseconds; 0 when the key is not cooling down. */
  cooldownUntil: number;
  lastStatus: number;
  requests: number;
  failures: number;
  quota: SearchKeyQuota | null;
  /** Out of quota or over budget; skipped by rotation. */
  exhausted: boolean;
}

type RawRecord = Record<string, unknown>;

const isRecord = (value: unknown): value is RawRecord =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const str = (value: unknown): string => (typeof value === 'string' ? value : '');

const num = (value: unknown): number =>
  typeof value === 'number' && Number.isFinite(value) ? value : 0;

const optionalNum = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) ? value : null;

const timestamp = (value: unknown): number => {
  if (typeof value !== 'string' || !value) return 0;
  const parsed = Date.parse(value);
  // Go serializes zero times as 0001-01-01T00:00:00Z.
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
};

export const parseSearchKeys = (data: unknown): SearchKeyEntry[] => {
  const list = isRecord(data) ? data['search-api-key'] : undefined;
  if (!Array.isArray(list)) return [];
  return list.filter(isRecord).flatMap((item) => {
    const apiKey = str(item['api-key']);
    const provider = str(item.provider);
    if (!apiKey || !provider) return [];
    return [
      {
        provider,
        apiKey,
        label: str(item.label),
        baseUrl: str(item['base-url']),
        proxyUrl: str(item['proxy-url']),
        disabled: item.disabled === true,
        budget: num(item.budget),
      },
    ];
  });
};

export const toSearchKeyPayload = (entry: SearchKeyEntry): RawRecord => {
  const payload: RawRecord = {
    provider: entry.provider.trim().toLowerCase(),
    'api-key': entry.apiKey.trim(),
  };
  const optional: Array<[string, string]> = [
    ['label', entry.label],
    ['base-url', entry.baseUrl],
    ['proxy-url', entry.proxyUrl],
  ];
  for (const [field, value] of optional) {
    if (value.trim()) payload[field] = value.trim();
  }
  if (entry.disabled) payload.disabled = true;
  if (entry.budget > 0) payload.budget = entry.budget;
  return payload;
};

export const parseSearchKeyStatuses = (data: unknown): SearchKeyStatus[] => {
  const list = isRecord(data) ? data.keys : undefined;
  if (!Array.isArray(list)) return [];
  return list.filter(isRecord).map((item) => ({
    id: str(item.id),
    index: num(item.index),
    provider: str(item.provider),
    maskedKey: str(item['masked-key']),
    cooldownUntil: timestamp(item['cooldown-until']),
    lastStatus: num(item['last-status']),
    requests: num(item.requests),
    failures: num(item.failures),
    quota: parseQuota(item.quota),
    exhausted: item.exhausted === true,
  }));
};

const parseQuota = (raw: unknown): SearchKeyQuota | null => {
  if (!isRecord(raw)) return null;
  return {
    unit: str(raw.unit),
    used: optionalNum(raw.used),
    limit: optionalNum(raw.limit),
    remaining: optionalNum(raw.remaining),
    plan: str(raw.plan),
    resetAt: timestamp(raw['reset-at']),
    checkedAt: timestamp(raw['checked-at']),
    error: str(raw.error),
  };
};

export interface SearchMcpSettings {
  /** Priority order used by web_search / web_fetch; unlisted providers are not used. */
  providerOrder: string[];
  /** Also expose the per-provider tools (tavily_*, exa_*, firecrawl_*). */
  exposeProviderTools: boolean;
}

export const parseSearchMcpSettings = (data: unknown): SearchMcpSettings => {
  const raw = isRecord(data) && isRecord(data['search-mcp']) ? data['search-mcp'] : {};
  const order = Array.isArray(raw['provider-order'])
    ? raw['provider-order'].filter(
        (item): item is string =>
          typeof item === 'string' && (SEARCH_PROVIDERS as readonly string[]).includes(item)
      )
    : [];
  return {
    providerOrder: order.length ? order : [...SEARCH_PROVIDERS],
    exposeProviderTools: raw['expose-provider-tools'] === true,
  };
};

export const toSearchMcpPayload = (settings: SearchMcpSettings): RawRecord => ({
  'provider-order': settings.providerOrder,
  'expose-provider-tools': settings.exposeProviderTools,
});

export const searchKeysApi = {
  async list(): Promise<SearchKeyEntry[]> {
    return parseSearchKeys(await apiClient.get<unknown>('/search-api-key'));
  },

  async status(): Promise<SearchKeyStatus[]> {
    return parseSearchKeyStatuses(await apiClient.get<unknown>('/search-api-key/status'));
  },

  replace: (entries: SearchKeyEntry[]) =>
    apiClient.put('/search-api-key', entries.map(toSearchKeyPayload)),

  update: (index: number, entry: SearchKeyEntry) =>
    apiClient.patch('/search-api-key', {
      index,
      value: {
        label: '',
        'base-url': '',
        'proxy-url': '',
        disabled: false,
        budget: 0,
        ...toSearchKeyPayload(entry),
      },
    }),

  remove: (index: number) => apiClient.delete(`/search-api-key?index=${index}`),

  async refreshQuota(
    target: { provider: string; apiKey: string } | 'all'
  ): Promise<SearchKeyStatus[]> {
    return parseSearchKeyStatuses(
      await apiClient.post('/search-api-key/refresh-quota', searchKeyTarget(target))
    );
  },

  async mcpSettings(): Promise<SearchMcpSettings> {
    return parseSearchMcpSettings(await apiClient.get<unknown>('/search-mcp'));
  },

  saveMcpSettings: (settings: SearchMcpSettings) =>
    apiClient.put('/search-mcp', toSearchMcpPayload(settings)),

  resetSpend: (target: { provider: string; apiKey: string }) =>
    apiClient.post('/search-api-key/reset-spend', searchKeyTarget(target)),

  resetCooldown: (target: { provider: string; apiKey: string } | 'all') =>
    apiClient.post('/search-api-key/reset-cooldown', searchKeyTarget(target)),
};

function searchKeyTarget(target: { provider: string; apiKey: string } | 'all'): RawRecord {
  return target === 'all' ? { all: true } : { provider: target.provider, 'api-key': target.apiKey };
}
