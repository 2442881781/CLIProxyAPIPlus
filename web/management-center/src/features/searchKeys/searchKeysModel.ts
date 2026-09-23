import {
  SEARCH_PROVIDERS,
  type SearchKeyEntry,
  type SearchKeyStatus,
} from '@/services/api/searchKeys';

export type SearchKeyState = 'active' | 'cooling' | 'disabled';

export interface SearchKeyRow {
  index: number;
  entry: SearchKeyEntry;
  status: SearchKeyStatus | null;
  state: SearchKeyState;
  cooldownRemainingMs: number;
}

export interface SearchProviderSummary {
  provider: string;
  total: number;
  active: number;
  cooling: number;
  disabled: number;
}

export const buildSearchKeyRows = (
  entries: SearchKeyEntry[],
  statuses: SearchKeyStatus[],
  nowMs: number
): SearchKeyRow[] => {
  const byIndex = new Map(statuses.map((status) => [status.index, status]));
  return entries.map((entry, index) => {
    const status = byIndex.get(index) ?? null;
    const cooldownRemainingMs =
      !entry.disabled && status && status.cooldownUntil > nowMs ? status.cooldownUntil - nowMs : 0;
    const state: SearchKeyState = entry.disabled
      ? 'disabled'
      : cooldownRemainingMs > 0
        ? 'cooling'
        : 'active';
    return { index, entry, status, state, cooldownRemainingMs };
  });
};

export const summarizeSearchProviders = (rows: SearchKeyRow[]): SearchProviderSummary[] =>
  SEARCH_PROVIDERS.map((provider) => {
    const own = rows.filter((row) => row.entry.provider === provider);
    return {
      provider,
      total: own.length,
      active: own.filter((row) => row.state === 'active').length,
      cooling: own.filter((row) => row.state === 'cooling').length,
      disabled: own.filter((row) => row.state === 'disabled').length,
    };
  });

/** Returns an i18n error key, or null when the form is valid. */
export const validateSearchKeyForm = (
  form: SearchKeyEntry,
  existing: SearchKeyEntry[],
  editingIndex?: number
): string | null => {
  const provider = form.provider.trim().toLowerCase();
  const apiKey = form.apiKey.trim();
  if (!(SEARCH_PROVIDERS as readonly string[]).includes(provider)) {
    return 'search_keys.error_provider';
  }
  if (!apiKey) return 'search_keys.error_key_required';
  const duplicate = existing.some(
    (entry, index) => index !== editingIndex && entry.provider === provider && entry.apiKey === apiKey
  );
  return duplicate ? 'search_keys.error_duplicate' : null;
};

export interface SearchUsageSnippets {
  restBase: Record<string, string>;
  mcpUrl: string;
  claudeMcpCommand: string;
  firecrawlMcpEnv: string;
}

export const buildSearchUsageSnippets = (apiBase: string): SearchUsageSnippets => {
  const base = apiBase.trim().replace(/\/+$/, '');
  const restBase = Object.fromEntries(
    SEARCH_PROVIDERS.map((provider) => [provider, `${base}/search/${provider}`])
  );
  const mcpUrl = `${base}/search/mcp`;
  return {
    restBase,
    mcpUrl,
    claudeMcpCommand: `claude mcp add --transport http cpa-search ${mcpUrl} --header "Authorization: Bearer <CPA_API_KEY>"`,
    firecrawlMcpEnv: `FIRECRAWL_API_URL=${restBase.firecrawl} FIRECRAWL_API_KEY=<CPA_API_KEY>`,
  };
};

const COOLDOWN_UNITS: Array<[string, number]> = [
  ['d', 86_400_000],
  ['h', 3_600_000],
  ['m', 60_000],
  ['s', 1_000],
];

/** Formats a remaining duration using its two largest non-zero units, e.g. "5h 59m". */
export const formatCooldown = (ms: number): string => {
  let rest = Math.max(0, Math.ceil(ms / 1_000) * 1_000);
  const parts: string[] = [];
  for (const [unit, size] of COOLDOWN_UNITS) {
    const value = Math.floor(rest / size);
    rest -= value * size;
    if (value > 0 || parts.length > 0) parts.push(`${value}${unit}`);
    if (parts.length === 2) break;
  }
  const trimmed = parts.filter((part, index) => index === 0 || !part.startsWith('0'));
  return trimmed.length ? trimmed.join(' ') : '0s';
};
