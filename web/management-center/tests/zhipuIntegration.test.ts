import { describe, expect, test } from 'bun:test';
import type { ProviderResource } from '../src/features/providers/types';
import {
  isZhipuCodingPlanResource,
  ZHIPU_CODING_PLAN_BASE_URL,
  ZHIPU_CODING_PLAN_TEMPLATE,
} from '../src/features/providers/zhipu';

const resource = (name: string, baseUrl: string): ProviderResource => ({
  ...ZHIPU_CODING_PLAN_TEMPLATE,
  id: `${name}:${baseUrl}`,
  name,
  baseUrl,
});

describe('Zhipu Coding Plan integration', () => {
  test('recognizes custom-named domestic Coding Plan entries by endpoint', () => {
    expect(
      isZhipuCodingPlanResource(resource('团队版', 'https://open.bigmodel.cn/api/coding/paas/v4'))
    ).toBe(true);
  });

  test('recognizes international Coding Plan entries and legacy names', () => {
    expect(isZhipuCodingPlanResource(resource('Team', 'https://api.z.ai/api/coding/paas/v4'))).toBe(
      true
    );
    expect(isZhipuCodingPlanResource(resource('Zhipu GLM', 'https://example.com/v1'))).toBe(true);
  });

  test('does not claim unrelated OpenAI-compatible providers', () => {
    expect(isZhipuCodingPlanResource(resource('Team', 'https://example.com/v1'))).toBe(false);
  });

  test('provides a create template with the official Coding Plan endpoint and model', () => {
    expect(ZHIPU_CODING_PLAN_TEMPLATE.brand).toBe('openaiCompatibility');
    expect(ZHIPU_CODING_PLAN_TEMPLATE.baseUrl).toBe(ZHIPU_CODING_PLAN_BASE_URL);
    expect(ZHIPU_CODING_PLAN_TEMPLATE.models).toEqual(['glm-5']);
  });
});
