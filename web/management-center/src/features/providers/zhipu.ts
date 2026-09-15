import type { ProviderResource } from './types';

export const ZHIPU_CODING_PLAN_NAME = 'Zhipu GLM Coding Plan';
export const ZHIPU_CODING_PLAN_BASE_URL = 'https://open.bigmodel.cn/api/coding/paas/v4';

export const isZhipuCodingPlanResource = (resource: ProviderResource): boolean => {
  const name = resource.name?.trim().toLowerCase() ?? '';
  const baseUrl = resource.baseUrl?.trim().toLowerCase() ?? '';
  return (
    name.includes('zhipu glm') ||
    baseUrl.includes('open.bigmodel.cn/api/coding/') ||
    baseUrl.includes('api.z.ai/api/coding/')
  );
};

export const ZHIPU_CODING_PLAN_TEMPLATE: ProviderResource = {
  id: 'integration:zhipu:new',
  brand: 'openaiCompatibility',
  originalIndex: -1,
  name: ZHIPU_CODING_PLAN_NAME,
  identifier: ZHIPU_CODING_PLAN_NAME,
  apiKeyPreview: null,
  apiKey: null,
  authIndex: null,
  baseUrl: ZHIPU_CODING_PLAN_BASE_URL,
  proxyUrl: null,
  prefix: 'zhipu',
  modelCount: 1,
  models: ['glm-5'],
  priority: 0,
  headerCount: 0,
  excludedModelCount: 0,
  apiKeyEntryCount: 0,
  disabled: false,
  flags: {},
  selector: {
    brand: 'openaiCompatibility',
    name: ZHIPU_CODING_PLAN_NAME,
    index: -1,
  },
  raw: {
    name: ZHIPU_CODING_PLAN_NAME,
    baseUrl: ZHIPU_CODING_PLAN_BASE_URL,
    prefix: 'zhipu',
    disabled: false,
    models: [{ name: 'glm-5', alias: '' }],
  },
};
