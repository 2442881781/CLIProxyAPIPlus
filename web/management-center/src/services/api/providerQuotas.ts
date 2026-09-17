import { apiClient } from './client';

// The server serializes normalized plugin buckets in camelCase; snake_case is
// kept for older snapshots and plugin payloads.
export interface ProviderQuotaBucketSnapshot {
  window?: string;
  remaining_fraction?: number;
  remainingFraction?: number;
  reset_time?: string;
  resetTime?: string;
}

export interface ProviderQuotaSourceSnapshot {
  id: string;
  label?: string;
  remaining_percent?: number;
  observed_at?: string;
  last_attempt_at?: string;
  last_error?: string;
  quota?: {
    groups?: Array<{
      display_name?: string;
      displayName?: string;
      buckets?: ProviderQuotaBucketSnapshot[];
    }>;
  };
}

export interface ProviderQuotaSnapshot {
  provider: string;
  remaining_percent: number;
  observed_at?: string;
  last_attempt_at?: string;
  last_error?: string;
  sources?: ProviderQuotaSourceSnapshot[];
}

export const providerQuotasApi = {
  async list(): Promise<ProviderQuotaSnapshot[]> {
    const response = (await apiClient.get('/provider-quotas')) as {
      providers?: ProviderQuotaSnapshot[];
    };
    return Array.isArray(response.providers) ? response.providers : [];
  },

  refresh(provider: string): Promise<ProviderQuotaSnapshot> {
    return apiClient.post(`/provider-quotas/${encodeURIComponent(provider)}/refresh`);
  },
};
