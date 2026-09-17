import type { TFunction } from 'i18next';
import type {
  AuthFileItem,
  ProviderQuotaData,
  ProviderQuotaResetCredit,
  ProviderQuotaResetType,
  ProviderQuotaState,
  ProviderQuotaWindow,
} from '@/types';
import {
  apiCallApi,
  authFilesApi,
  getApiCallErrorMessage,
  providerQuotasApi,
  type ProviderQuotaSnapshot,
  type ProviderQuotaSourceSnapshot,
} from '@/services/api';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { createStatusError, isDisabledAuthFile } from '@/utils/quota';
import { readProviderQuotaCredential } from '../../providerQuotaSources';
import type { QuotaProviderData, QuotaResetAction } from '../types';

const ZHIPU_CN_QUOTA_URL = 'https://open.bigmodel.cn/api/monitor/usage/quota/limit';
const ZHIPU_TEAM_QUOTA_URL = `${ZHIPU_CN_QUOTA_URL}?type=2`;
const ZHIPU_GLOBAL_QUOTA_URL = 'https://api.z.ai/api/monitor/usage/quota/limit';
const ZHIPU_CN_API_ORIGIN = 'https://open.bigmodel.cn';
const ZHIPU_GLOBAL_API_ORIGIN = 'https://api.z.ai';
const ZHIPU_RESET_LIST_PATH = '/api/biz/customer-package-reset/list';
const ZHIPU_RESET_USE_PATH = '/api/biz/customer-package-reset/use';

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const asNumber = (value: unknown): number | null => {
  const number =
    typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN;
  return Number.isFinite(number) ? number : null;
};

const asPercentNumber = (value: unknown): number | null => {
  if (typeof value === 'string' && value.trim().endsWith('%')) {
    return asNumber(value.trim().slice(0, -1));
  }
  return asNumber(value);
};

const clampPercent = (value: number): number => Math.min(100, Math.max(0, value));

const asResetMs = (value: unknown): number | null => {
  const numeric = asNumber(value);
  if (numeric !== null) return numeric < 10_000_000_000 ? numeric * 1000 : numeric;
  if (typeof value !== 'string' || !value.trim()) return null;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
};

const requestJSON = async (
  url: string,
  apiKey: string,
  proxyUrl?: string,
  extraHeaders: Record<string, string> = {},
  method = 'GET',
  data?: string
): Promise<Record<string, unknown>> => {
  const result = await apiCallApi.request({
    method,
    url,
    header: { ...extraHeaders, Authorization: `Bearer ${apiKey}`, Accept: 'application/json' },
    ...(data !== undefined ? { data } : {}),
    proxyUrl,
  });
  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
  }
  if (!isRecord(result.body)) throw new Error('Invalid quota response');
  return result.body;
};

const makePercentWindow = (
  id: string,
  label: string,
  usedPercent: number,
  resetAtMs?: number | null
): ProviderQuotaWindow => ({
  id,
  label,
  usedPercent: clampPercent(usedPercent),
  remainingPercent: clampPercent(100 - usedPercent),
  resetAtMs,
});

const readPathRecord = (
  root: Record<string, unknown>,
  keys: string[]
): Record<string, unknown> | null => {
  let value: unknown = root;
  for (const key of keys) {
    if (!isRecord(value)) return null;
    value = value[key];
  }
  return isRecord(value) ? value : null;
};

const parseCommandCodeQuota = (
  payload: Record<string, unknown>,
  t: TFunction
): ProviderQuotaData => {
  const data = isRecord(payload.data) ? payload.data : payload;
  const windowsRoot =
    readPathRecord(data, ['windowLimits']) ?? readPathRecord(data, ['window_limits']) ?? data;
  const windows: ProviderQuotaWindow[] = [];

  const readWindow = (id: string, labelKey: string, ...keys: string[]) => {
    const raw = keys.map((key) => windowsRoot[key]).find(isRecord);
    if (!raw) return;
    const used = asNumber(raw.used ?? raw.current ?? raw.usage);
    const limit = asNumber(raw.limit ?? raw.total ?? raw.cap);
    const explicitPercent = asNumber(raw.percent ?? raw.percentage ?? raw.used_percent);
    const usedPercent =
      explicitPercent ??
      (used !== null && limit !== null && limit > 0 ? (used / limit) * 100 : null);
    if (usedPercent === null) return;
    windows.push({
      ...makePercentWindow(id, t(labelKey), usedPercent, asResetMs(raw.resetAt ?? raw.reset_at)),
      ...(used !== null ? { usedValue: used } : {}),
      ...(limit !== null ? { limitValue: limit } : {}),
    });
  };

  readWindow(
    'fiveHour',
    'provider_quota.windows.five_hour',
    'fiveHour',
    'five_hour',
    'five_hour_limit'
  );
  readWindow('weekly', 'provider_quota.windows.weekly', 'weekly', 'weekly_limit');

  const credits = isRecord(data.credits) ? data.credits : data;
  const balance = asNumber(
    credits.monthlyCredits ??
      credits.monthly_credits ??
      credits.remainingCredits ??
      credits.remaining_credits
  );
  if (windows.length === 0 && balance === null) throw new Error(t('provider_quota.empty_data'));
  return {
    windows,
    ...(balance !== null ? { balance, balanceUnit: 'USD' } : {}),
  };
};

const parseOpenCodeQuota = (payload: Record<string, unknown>, t: TFunction): ProviderQuotaData => {
  const usage = isRecord(payload.usage) ? payload.usage : payload;
  const specs: Array<[string, string]> = [
    ['rolling', 'provider_quota.windows.rolling'],
    ['weekly', 'provider_quota.windows.weekly'],
    ['monthly', 'provider_quota.windows.monthly'],
  ];
  const windows = specs.flatMap(([id, labelKey]) => {
    const raw = usage[id];
    if (!isRecord(raw)) return [];
    const usedPercent = asNumber(raw.percent ?? raw.percentage);
    if (usedPercent === null) return [];
    return [
      makePercentWindow(id, t(labelKey), usedPercent, asResetMs(raw.resetsAt ?? raw.resets_at)),
    ];
  });
  if (windows.length === 0) throw new Error(t('provider_quota.empty_data'));
  return { windows };
};

const tokenWindowKind = (entry: Record<string, unknown>): 'session' | 'weekly' | null => {
  const unit = asNumber(entry.unit);
  const count = asNumber(entry.number);
  if (unit === null || count === null || count <= 0) return null;
  const minutesByUnit: Record<number, number> = {
    1: 24 * 60,
    3: 60,
    4: 24 * 60,
    5: 1,
    6: 7 * 24 * 60,
  };
  const minutes = minutesByUnit[unit] * count;
  if (!Number.isFinite(minutes)) return null;
  return minutes <= 6 * 60 ? 'session' : 'weekly';
};

const zhipuUsedPercent = (entry: Record<string, unknown>): number | null => {
  const total = asNumber(entry.usage);
  const remaining = asNumber(entry.remaining);
  const current = asNumber(entry.currentValue ?? entry.current_value);
  if (total !== null && total > 0) {
    const usedFromRemaining = remaining === null ? null : total - remaining;
    const used = Math.max(0, Math.min(total, Math.max(current ?? 0, usedFromRemaining ?? 0)));
    return (used / total) * 100;
  }
  return asNumber(entry.percentage ?? entry.usedPercent ?? entry.used_percent);
};

const assertZhipuBusinessSuccess = (payload: Record<string, unknown>, t: TFunction): void => {
  const code = asNumber(payload.code);
  if (payload.success !== false && (code === null || code === 0 || code === 200)) return;
  const message = [payload.msg, payload.message].find(
    (value): value is string => typeof value === 'string' && Boolean(value.trim())
  );
  throw new Error(message?.trim() || t('provider_quota.empty_data'));
};

const parseZhipuQuota = (payload: Record<string, unknown>, t: TFunction): ProviderQuotaData => {
  assertZhipuBusinessSuccess(payload, t);

  const data = isRecord(payload.data) ? payload.data : payload;
  const limits = Array.isArray(data.limits) ? data.limits.filter(isRecord) : [];
  const windows: ProviderQuotaWindow[] = [];

  for (const entry of limits) {
    const type = String(entry.type ?? entry.limit_type ?? entry.name ?? '').toUpperCase();
    if (type === 'TOKENS_LIMIT' || type === 'CREDIT_LIMIT') {
      const kind = tokenWindowKind(entry);
      const usedPercent = zhipuUsedPercent(entry);
      if (!kind || usedPercent === null || windows.some((window) => window.id === kind)) continue;
      windows.push(
        makePercentWindow(
          kind,
          t(`provider_quota.windows.${kind}`),
          usedPercent,
          asResetMs(entry.nextResetTime ?? entry.next_reset_time)
        )
      );
    } else if (type === 'TIME_LIMIT') {
      const used = asNumber(entry.currentValue ?? entry.current_value);
      const limit = asNumber(entry.usage);
      const usedPercent = zhipuUsedPercent(entry);
      if (usedPercent === null) continue;
      windows.push({
        ...makePercentWindow(
          'mcp',
          t('provider_quota.windows.mcp'),
          usedPercent,
          asResetMs(entry.nextResetTime ?? entry.next_reset_time)
        ),
        ...(used !== null && used >= 0 ? { usedValue: used } : {}),
        ...(limit !== null && limit >= 0 ? { limitValue: limit } : {}),
        ...(used !== null && limit !== null ? { unit: t('provider_quota.searches') } : {}),
      });
    }
  }
  if (windows.length === 0) throw new Error(t('provider_quota.empty_data'));
  const planValue = data.level ?? data.planName ?? data.plan_name ?? data.plan;
  const plan = typeof planValue === 'string' ? planValue.trim() : '';
  return { windows, ...(plan ? { plan } : {}) };
};

const asZhipuExpiryMs = (value: unknown): number | null => {
  if (typeof value === 'string') {
    const trimmed = value.trim();
    if (/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(trimmed)) {
      return asResetMs(`${trimmed.replace(' ', 'T')}+08:00`);
    }
  }
  return asResetMs(value);
};

const parseZhipuResetCredits = (
  payload: Record<string, unknown>,
  t: TFunction
): ProviderQuotaResetCredit[] => {
  assertZhipuBusinessSuccess(payload, t);
  const data = isRecord(payload.data) ? payload.data : payload;
  const credits: ProviderQuotaResetCredit[] = [];
  const appendCredits = (source: unknown, type: ProviderQuotaResetType) => {
    if (!Array.isArray(source)) return;
    source.filter(isRecord).forEach((entry) => {
      const rawRecordId = entry.recordId ?? entry.record_id;
      if (rawRecordId === undefined || rawRecordId === null) return;
      const recordId = String(rawRecordId).trim();
      if (!recordId) return;
      credits.push({
        actionId: `${type}:${recordId}`,
        type,
        recordId,
        expiresAtMs: asZhipuExpiryMs(entry.expireTime ?? entry.expire_time),
        available: entry.available === true,
      });
    });
  };
  appendCredits(data.fiveHourResets ?? data.five_hour_resets, 'FIVE_HOUR');
  appendCredits(data.weekResets ?? data.week_resets, 'WEEK');
  return credits.sort((left, right) => {
    if (left.available !== right.available) return left.available ? -1 : 1;
    return (
      (left.expiresAtMs ?? Number.MAX_SAFE_INTEGER) - (right.expiresAtMs ?? Number.MAX_SAFE_INTEGER)
    );
  });
};

const readDevinSignals = (file: AuthFileItem): Record<string, unknown> | null => {
  const quota = file.quota;
  if (isRecord(quota) && isRecord(quota.signals)) return quota.signals;
  const rawQuota = file.Quota;
  if (isRecord(rawQuota) && isRecord(rawQuota.Signals)) return rawQuota.Signals;
  return null;
};

const parseDevinQuota = (file: AuthFileItem, t: TFunction): ProviderQuotaData => {
  const signals = readDevinSignals(file);
  if (!signals) throw new Error(t('provider_quota.empty_data'));
  const windows: ProviderQuotaWindow[] = [];
  const addRemainingWindow = (id: 'daily' | 'weekly') => {
    const remaining = asPercentNumber(
      signals[`${id}_quota_remaining_percent`] ?? signals[`${id}QuotaRemainingPercent`]
    );
    if (remaining === null) return;
    windows.push({
      id,
      label: t(`provider_quota.windows.${id}`),
      usedPercent: clampPercent(100 - remaining),
      remainingPercent: clampPercent(remaining),
      resetAtMs: asResetMs(signals[`${id}_quota_reset_at`] ?? signals[`${id}QuotaResetAt`]),
    });
  };
  addRemainingWindow('daily');
  addRemainingWindow('weekly');
  if (windows.length === 0) throw new Error(t('provider_quota.empty_data'));
  const plan = signals.plan;
  return { windows, ...(typeof plan === 'string' && plan.trim() ? { plan: plan.trim() } : {}) };
};

const snapshotSourceToQuotaData = (
  source: ProviderQuotaSourceSnapshot,
  t: TFunction,
  fallbackReason = ''
): ProviderQuotaData => {
  const windows = (source.quota?.groups ?? []).flatMap((group) =>
    (group.buckets ?? []).map((bucket, index) => {
      const remaining = clampPercent((asNumber(bucket.remaining_fraction) ?? 0) * 100);
      const id = String(bucket.window || `window-${index}`);
      return makePercentWindow(
        id,
        t(`provider_quota.windows.${id}`, { defaultValue: id }),
        100 - remaining,
        asResetMs(bucket.reset_time)
      );
    })
  );
  if (windows.length === 0) {
    throw new Error(
      source.last_error?.trim() || fallbackReason.trim() || t('provider_quota.empty_data')
    );
  }
  return { windows };
};

const fetchServerProviderQuota = async (
  file: AuthFileItem,
  provider: string,
  t: TFunction
): Promise<ProviderQuotaData> => {
  let snapshot: ProviderQuotaSnapshot;
  let refreshError: unknown;
  try {
    snapshot = await providerQuotasApi.refresh(provider);
  } catch (error: unknown) {
    // Keep the server's reason: the request layer turns the response body's
    // `error` field into the thrown message, so surfacing it beats the generic
    // "no quota data" text that used to hide every refresh failure.
    refreshError = error;
    let current: ProviderQuotaSnapshot | undefined;
    try {
      current = (await providerQuotasApi.list()).find((item) => item.provider === provider);
    } catch {
      current = undefined;
    }
    if (!current) {
      throw error instanceof Error ? error : new Error(t('provider_quota.empty_data'));
    }
    snapshot = current;
  }
  const source = (snapshot.sources ?? []).find((item) => item.label === file.name.split(' · ')[0]);
  const resolved = source ?? snapshot.sources?.find((item) => item.observed_at && !item.last_error);
  const refreshReason = refreshError instanceof Error ? refreshError.message.trim() : '';
  if (!resolved) {
    const reason = (snapshot.last_error ?? '').trim() || refreshReason;
    throw new Error(reason || t('provider_quota.empty_data'));
  }
  return snapshotSourceToQuotaData(resolved, t, refreshReason);
};

const fetchCommandCode = (file: AuthFileItem, t: TFunction): Promise<ProviderQuotaData> =>
  fetchServerProviderQuota(file, 'commandcode', t);

const fetchOpenCode = (file: AuthFileItem, t: TFunction): Promise<ProviderQuotaData> =>
  fetchServerProviderQuota(file, 'opencode-go', t);

const resolveZhipuRequestContext = (file: AuthFileItem, t: TFunction) => {
  const credential = readProviderQuotaCredential(file);
  if (!credential) throw new Error(t('provider_quota.missing_credential'));
  const targetType = credential.planKind === 'team' ? 'TEAM' : 'PERSONAL';
  const extraHeaders: Record<string, string> = {};
  if (targetType === 'TEAM') {
    const organization = credential.headers?.['bigmodel-organization'];
    const project = credential.headers?.['bigmodel-project'];
    if (!organization || !project) throw new Error(t('provider_quota.zhipu_team_ids_required'));
    extraHeaders['bigmodel-organization'] = organization;
    extraHeaders['bigmodel-project'] = project;
  }
  const apiOrigin = credential.baseUrl?.includes('api.z.ai')
    ? ZHIPU_GLOBAL_API_ORIGIN
    : ZHIPU_CN_API_ORIGIN;
  const quotaUrl =
    targetType === 'TEAM'
      ? ZHIPU_TEAM_QUOTA_URL
      : apiOrigin === ZHIPU_GLOBAL_API_ORIGIN
        ? ZHIPU_GLOBAL_QUOTA_URL
        : ZHIPU_CN_QUOTA_URL;
  return { credential, targetType, extraHeaders, apiOrigin, quotaUrl };
};

const errorMessage = (error: unknown, fallback: string): string =>
  error instanceof Error && error.message ? error.message : fallback;

const fetchZhipu = async (file: AuthFileItem, t: TFunction): Promise<ProviderQuotaData> => {
  const context = resolveZhipuRequestContext(file, t);
  const resetCreditsPromise = requestJSON(
    `${context.apiOrigin}${ZHIPU_RESET_LIST_PATH}?targetType=${context.targetType}`,
    context.credential.apiKey,
    context.credential.proxyUrl,
    context.extraHeaders
  )
    .then((payload) => ({
      resetCredits: parseZhipuResetCredits(payload, t),
      resetCreditsError: '',
    }))
    .catch((error: unknown) => ({
      resetCredits: [] as ProviderQuotaResetCredit[],
      resetCreditsError: errorMessage(error, t('provider_quota.reset_credits_load_failed')),
    }));
  const [quotaPayload, resetData] = await Promise.all([
    requestJSON(
      context.quotaUrl,
      context.credential.apiKey,
      context.credential.proxyUrl,
      context.extraHeaders
    ),
    resetCreditsPromise,
  ]);
  return { ...parseZhipuQuota(quotaPayload, t), ...resetData };
};

const createZhipuResetRequestId = (): string => {
  if (typeof globalThis.crypto?.randomUUID === 'function') return globalThis.crypto.randomUUID();
  return `zhipu-${Date.now()}-${Math.random().toString(16).slice(2)}`;
};

const parseZhipuResetActionId = (
  actionId: string | undefined,
  t: TFunction
): { type: ProviderQuotaResetType; recordId: string } => {
  const [type, ...recordIdParts] = String(actionId ?? '').split(':');
  const recordId = recordIdParts.join(':').trim();
  if ((type !== 'FIVE_HOUR' && type !== 'WEEK') || !recordId) {
    throw new Error(t('provider_quota.reset_credit_unavailable'));
  }
  return { type, recordId };
};

const resetZhipuQuota = async (
  file: AuthFileItem,
  t: TFunction,
  actionId?: string
): Promise<ProviderQuotaData> => {
  const context = resolveZhipuRequestContext(file, t);
  const action = parseZhipuResetActionId(actionId, t);
  const recordId = /^\d+$/.test(action.recordId) ? Number(action.recordId) : action.recordId;
  const payload = await requestJSON(
    `${context.apiOrigin}${ZHIPU_RESET_USE_PATH}`,
    context.credential.apiKey,
    context.credential.proxyUrl,
    { ...context.extraHeaders, 'Content-Type': 'application/json' },
    'POST',
    JSON.stringify({
      targetType: context.targetType,
      resetType: action.type,
      recordId,
      requestId: createZhipuResetRequestId(),
    })
  );
  assertZhipuBusinessSuccess(payload, t);
  return fetchZhipu(file, t);
};

const getZhipuResetActions = (quota: ProviderQuotaState, t: TFunction): QuotaResetAction[] => {
  const credits = quota.resetCredits ?? [];
  const definitions: Array<{
    type: ProviderQuotaResetType;
    windowId: string;
    labelKey: string;
  }> = [
    { type: 'FIVE_HOUR', windowId: 'session', labelKey: 'provider_quota.reset_five_hour' },
    { type: 'WEEK', windowId: 'weekly', labelKey: 'provider_quota.reset_week' },
  ];
  return definitions.flatMap(({ type, windowId, labelKey }) => {
    const hasUsage = (quota.windows.find((window) => window.id === windowId)?.usedPercent ?? 0) > 0;
    if (!hasUsage) return [];
    const credit = credits.find((item) => item.type === type && item.available);
    if (!credit) return [];
    const quotaLabel = t(labelKey);
    return [
      {
        id: credit.actionId,
        buttonLabel: t('provider_quota.use_reset_button', { quota: quotaLabel }),
        confirmTitle: t('provider_quota.reset_confirm_title'),
        confirmMessage: t('provider_quota.reset_confirm_message', { quota: quotaLabel }),
        confirmButton: t('provider_quota.reset_confirm_button'),
        successMessage: t('provider_quota.reset_success', { quota: quotaLabel }),
      },
    ];
  });
};

const fetchDevin = async (file: AuthFileItem, t: TFunction): Promise<ProviderQuotaData> => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  await authFilesApi.forceRefresh(file.name, authIndex || undefined);
  const refreshedFiles = (await authFilesApi.list()).files ?? [];
  const refreshed = refreshedFiles.find(
    (candidate) =>
      candidate.name === file.name ||
      (authIndex &&
        normalizeAuthIndex(candidate['auth_index'] ?? candidate.authIndex) === authIndex)
  );
  if (!refreshed) throw new Error(t('provider_quota.empty_data'));
  return parseDevinQuota(refreshed, t);
};

const STORE_SETTERS = {
  commandcode: 'setCommandcodeQuota',
  opencode: 'setOpencodeQuota',
  zhipu: 'setZhipuQuota',
  devin: 'setDevinQuota',
} as const;

const createConfig = (
  type: 'commandcode' | 'opencode' | 'zhipu' | 'devin',
  fetchQuota: (file: AuthFileItem, t: TFunction) => Promise<ProviderQuotaData>
): QuotaProviderData<ProviderQuotaState, ProviderQuotaData> => ({
  type,
  i18nPrefix: 'provider_quota',
  filterFn: (file) =>
    String(file.type ?? file.provider ?? '').toLowerCase() === type && !isDisabledAuthFile(file),
  fetchQuota,
  storeSelector: (state) => state[`${type}Quota`],
  storeSetter: STORE_SETTERS[type],
  buildLoadingState: () => ({ status: 'loading', windows: [] }),
  buildSuccessState: (data) => ({ status: 'success', ...data }),
  buildErrorState: (message, status) => ({
    status: 'error',
    windows: [],
    error: message,
    errorStatus: status,
  }),
});

export const COMMANDCODE_CONFIG = createConfig('commandcode', fetchCommandCode);
export const OPENCODE_CONFIG = createConfig('opencode', fetchOpenCode);
export const ZHIPU_CONFIG: QuotaProviderData<ProviderQuotaState, ProviderQuotaData> = {
  ...createConfig('zhipu', fetchZhipu),
  resetQuota: resetZhipuQuota,
  getResetActions: getZhipuResetActions,
};
export const DEVIN_CONFIG = createConfig('devin', fetchDevin);

export const providerQuotaParsers = {
  commandcode: parseCommandCodeQuota,
  opencode: parseOpenCodeQuota,
  zhipu: parseZhipuQuota,
  zhipuResetCredits: parseZhipuResetCredits,
  devin: parseDevinQuota,
};
