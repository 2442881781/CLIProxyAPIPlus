import type { AuthFileItem, OpenAIProviderConfig, PluginConfigObject } from '@/types';

interface ProviderQuotaCredential {
  apiKey: string;
  baseUrl?: string;
  proxyUrl?: string;
  providerName?: string;
  planKind?: 'personal' | 'team';
  headers?: Record<string, string>;
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const readString = (value: unknown): string => (typeof value === 'string' ? value.trim() : '');

const normalizeHeaders = (value: unknown): Record<string, string> => {
  if (!isRecord(value)) return {};
  return Object.fromEntries(
    Object.entries(value).flatMap(([key, headerValue]) => {
      const normalizedKey = key.trim().toLowerCase();
      const normalizedValue = readString(headerValue);
      return normalizedKey && normalizedValue ? [[normalizedKey, normalizedValue]] : [];
    })
  );
};

const isZhipuTeamPlan = (provider: OpenAIProviderConfig): boolean => {
  const headers = normalizeHeaders(provider.headers);
  return (
    /(?:团队|team)/i.test(provider.name) ||
    Boolean(headers['bigmodel-organization'] || headers['bigmodel-project'])
  );
};

const maskedSuffix = (key: string): string => {
  if (key.startsWith('${') && key.endsWith('}')) return key;
  const suffix = key.slice(-4);
  return suffix ? `••••${suffix}` : '••••';
};

const syntheticFile = (
  type: 'commandcode' | 'opencode' | 'zhipu',
  index: number,
  credential: ProviderQuotaCredential,
  disabled: boolean
): AuthFileItem => ({
  name: `${type === 'commandcode' ? 'CommandCode' : type === 'opencode' ? 'OpenCode Go' : 'Zhipu GLM'} #${index + 1}${type === 'zhipu' && credential.providerName ? ` · ${credential.providerName}` : ''} · ${maskedSuffix(credential.apiKey)}`,
  type,
  provider: type,
  disabled,
  runtimeOnly: true,
  status: disabled ? 'disabled' : 'active',
  quotaCredential: credential,
});

export const buildCommandCodeQuotaFiles = (config: PluginConfigObject): AuthFileItem[] => {
  const enabled = config.enabled !== false;
  const pooled = Array.isArray(config.api_keys)
    ? config.api_keys.flatMap((entry) => {
        if (!isRecord(entry)) return [];
        const apiKey = readString(entry.key);
        if (!apiKey) return [];
        return [
          {
            apiKey,
            proxyUrl: readString(entry.proxy_url) || undefined,
          },
        ];
      })
    : [];
  const credentials =
    pooled.length > 0
      ? pooled
      : readString(config.api_key)
        ? [{ apiKey: readString(config.api_key) }]
        : [];
  return credentials.map((credential, index) =>
    syntheticFile('commandcode', index, credential, !enabled)
  );
};

export const buildOpenCodeQuotaFiles = (config: PluginConfigObject): AuthFileItem[] => {
  const enabled = config.enabled !== false;
  const baseUrl = readString(config['base-url']) || 'https://opencode.ai/zen/go/v1';
  const credentials = Array.isArray(config['api-keys'])
    ? config['api-keys'].flatMap((entry) => {
        if (!isRecord(entry)) return [];
        const apiKey = readString(entry.value);
        return apiKey ? [{ apiKey, baseUrl }] : [];
      })
    : [];
  return credentials.map((credential, index) =>
    syntheticFile('opencode', index, credential, !enabled)
  );
};

const isZhipuCodingPlan = (provider: OpenAIProviderConfig): boolean => {
  const name = provider.name.trim().toLowerCase();
  const baseUrl = provider.baseUrl.trim().toLowerCase();
  return (
    name.includes('zhipu glm') ||
    name.includes('glm coding') ||
    baseUrl.includes('open.bigmodel.cn/api/coding/') ||
    baseUrl.includes('api.z.ai/api/coding/')
  );
};

export const buildZhipuQuotaFiles = (providers: OpenAIProviderConfig[]): AuthFileItem[] =>
  providers
    .filter(isZhipuCodingPlan)
    .flatMap((provider) =>
      (provider.apiKeyEntries ?? []).flatMap((entry) => {
        const apiKey = entry.apiKey.trim();
        if (!apiKey) return [];
        return [
          {
            credential: {
              apiKey,
              baseUrl: provider.baseUrl,
              proxyUrl: entry.proxyUrl,
              providerName: provider.name,
              planKind: isZhipuTeamPlan(provider) ? ('team' as const) : ('personal' as const),
              headers: normalizeHeaders(provider.headers),
            },
            disabled: provider.disabled === true,
          },
        ];
      })
    )
    .map(({ credential, disabled }, index) => syntheticFile('zhipu', index, credential, disabled));

export const buildAIProviderQuotaFiles = (
  authFiles: AuthFileItem[],
  commandCodeConfig: PluginConfigObject,
  openCodeConfig: PluginConfigObject,
  openAIProviders: OpenAIProviderConfig[]
): AuthFileItem[] => [
  ...authFiles,
  ...buildCommandCodeQuotaFiles(commandCodeConfig),
  ...buildOpenCodeQuotaFiles(openCodeConfig),
  ...buildZhipuQuotaFiles(openAIProviders),
];

export const readProviderQuotaCredential = (file: AuthFileItem): ProviderQuotaCredential | null => {
  const value = file.quotaCredential;
  if (!isRecord(value)) return null;
  const apiKey = readString(value.apiKey);
  if (!apiKey) return null;
  const baseUrl = readString(value.baseUrl);
  const proxyUrl = readString(value.proxyUrl);
  const providerName = readString(value.providerName);
  const planKind = readString(value.planKind);
  const headers = normalizeHeaders(value.headers);
  return {
    apiKey,
    ...(baseUrl ? { baseUrl } : {}),
    ...(proxyUrl ? { proxyUrl } : {}),
    ...(providerName ? { providerName } : {}),
    ...(planKind === 'team' || planKind === 'personal' ? { planKind } : {}),
    ...(Object.keys(headers).length ? { headers } : {}),
  };
};
