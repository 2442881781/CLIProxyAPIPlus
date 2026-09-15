import type { UsageMonitorProviderStats } from '@/types';

export const filterUsageProviders = (
  providers: UsageMonitorProviderStats[],
  selectedProvider: string,
  query: string
): UsageMonitorProviderStats[] => {
  const normalizedQuery = query.trim().toLowerCase();
  return providers.flatMap((provider) => {
    if (selectedProvider !== 'all' && provider.provider !== selectedProvider) return [];
    if (!normalizedQuery) return [provider];
    if (provider.provider.toLowerCase().includes(normalizedQuery)) return [provider];
    const models = provider.models.filter((model) =>
      model.model.toLowerCase().includes(normalizedQuery)
    );
    return models.length > 0 ? [{ ...provider, models }] : [];
  });
};

export const usageSuccessRate = (success: number, requests: number): number | null =>
  requests > 0 ? (success / requests) * 100 : null;
