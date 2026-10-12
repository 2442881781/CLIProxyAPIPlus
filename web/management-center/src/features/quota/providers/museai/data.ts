import type { TFunction } from 'i18next';
import { apiClient } from '@/services/api/client';
import { useAuthStore } from '@/stores/useAuthStore';
import type {
  AuthFileItem,
  ProviderQuotaData,
  ProviderQuotaState,
  ProviderQuotaWindow,
} from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { normalizeApiBase } from '@/utils/connection';
import { isDisabledAuthFile } from '@/utils/quota';
import type { QuotaProviderData } from '../types';

export const parseMuseaiQuota = (payload: unknown, t: TFunction): ProviderQuotaData => {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    throw new Error(t('provider_quota.empty_data'));
  }
  const subscription = 'subscription' in payload ? payload.subscription : null;
  let plan: string | undefined;
  if (subscription && typeof subscription === 'object') {
    plan = [
      'plan' in subscription ? subscription.plan : null,
      'tierName' in subscription ? subscription.tierName : null,
      'tier_name' in subscription ? subscription.tier_name : null,
    ].find((value): value is string => typeof value === 'string' && Boolean(value.trim()));
  }
  const groups = 'groups' in payload && Array.isArray(payload.groups) ? payload.groups : [];
  const windows: ProviderQuotaWindow[] = [];
  for (const group of groups) {
    if (
      !group ||
      typeof group !== 'object' ||
      !('buckets' in group) ||
      !Array.isArray(group.buckets)
    )
      continue;
    for (const bucket of group.buckets) {
      if (!bucket || typeof bucket !== 'object') continue;
      const remaining =
        'remainingFraction' in bucket
          ? bucket.remainingFraction
          : 'remaining_fraction' in bucket
            ? bucket.remaining_fraction
            : undefined;
      if (
        typeof remaining !== 'number' ||
        !Number.isFinite(remaining) ||
        remaining < 0 ||
        remaining > 1
      )
        continue;
      const id = 'window' in bucket && typeof bucket.window === 'string' ? bucket.window : '';
      if (!id) continue;
      const rawReset =
        'resetTime' in bucket
          ? bucket.resetTime
          : 'reset_time' in bucket
            ? bucket.reset_time
            : undefined;
      const reset = typeof rawReset === 'string' ? Date.parse(rawReset) : NaN;
      const description =
        'description' in bucket && typeof bucket.description === 'string'
          ? bucket.description.trim()
          : '';
      windows.push({
        id,
        label: t(`provider_quota.windows.${id}`, { defaultValue: id }),
        usedPercent: (1 - remaining) * 100,
        remainingPercent: remaining * 100,
        resetAtMs: Number.isFinite(reset) ? reset : null,
        periodHours: id === 'weekly' ? 168 : null,
        ...(description ? { description } : {}),
      });
    }
  }
  const unlimited = 'unlimited' in payload && payload.unlimited === true;
  if (windows.length === 0 && !unlimited) throw new Error(t('provider_quota.empty_data'));
  return { windows, ...(plan ? { plan: plan.trim() } : {}), ...(unlimited ? { unlimited } : {}) };
};

const fetchMuseaiQuota = async (file: AuthFileItem, t: TFunction): Promise<ProviderQuotaData> => {
  const authIndex = normalizeAuthIndex(file.auth_index ?? file.authIndex);
  if (!authIndex) throw new Error(t('provider_quota.missing_credential'));
  // apiClient's default base is legacy v0. An absolute v8 URL keeps management
  // authentication and error handling without introducing a v0 Muse endpoint.
  const base = normalizeApiBase(useAuthStore.getState().apiBase);
  const payload = await apiClient.post<unknown>(`${base}/v8/management/plugins/museai/quota`, {
    auth_index: authIndex,
  });
  return parseMuseaiQuota(payload, t);
};

export const MUSEAI_CONFIG: QuotaProviderData<ProviderQuotaState, ProviderQuotaData> = {
  type: 'museai',
  i18nPrefix: 'provider_quota',
  filterFn: (file) =>
    String(file.type ?? file.provider ?? '').toLowerCase() === 'museai' &&
    !isDisabledAuthFile(file),
  fetchQuota: fetchMuseaiQuota,
  storeSelector: (state) => state.museaiQuota,
  storeSetter: 'setMuseaiQuota',
  buildLoadingState: () => ({ status: 'loading', windows: [] }),
  buildSuccessState: (data) => ({ status: 'success', ...data }),
  buildErrorState: (message, status) => ({
    status: 'error',
    windows: [],
    error: message,
    errorStatus: status,
  }),
};
