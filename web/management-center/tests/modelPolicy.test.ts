import { describe, expect, it } from 'bun:test';
import {
  modelPolicySignature,
  normalizeModelNames,
  normalizeModelPolicy,
  readModelPolicyAliases,
} from '../src/features/providers/modelPolicy';
import { normalizeOauthAllowedModels } from '../src/services/api/authFiles';
import {
  mergeProviderModelCatalogs,
  normalizeProviderModels,
} from '../src/services/api/models';

describe('provider model policy', () => {
  it('preserves custom values while deduplicating model names case-insensitively', () => {
    expect(normalizeModelNames([' gpt-* ', 'GPT-*', 'custom/model', ''])).toEqual([
      'gpt-*',
      'custom/model',
    ]);
  });

  it('reads aliases and force-mapping from backend payloads', () => {
    expect(
      readModelPolicyAliases([
        { name: 'upstream/a', alias: 'friendly', fork: true, 'force-mapping': true },
        { name: 'upstream/b', alias: 'FRIENDLY' },
      ])
    ).toEqual([{ name: 'upstream/a', alias: 'friendly', fork: true, forceMapping: true }]);
  });

  it('compares policy content independently of ordering and casing', () => {
    const left = normalizeModelPolicy({
      allowedModels: ['Model-B', 'model-a'],
      aliases: [{ name: 'upstream', alias: 'friendly' }],
    });
    const right = normalizeModelPolicy({
      allowedModels: ['MODEL-A', 'model-b'],
      aliases: [{ name: 'upstream', alias: 'friendly' }],
    });
    expect(modelPolicySignature(left)).toBe(modelPolicySignature(right));
  });

  it('normalizes allowed-model management responses', () => {
    expect(
      normalizeOauthAllowedModels({
        'oauth-allowed-models': { ' OpenCode-Go ': [' model-a ', 'MODEL-A', 'gpt-*'] },
      })
    ).toEqual({ 'opencode-go': ['model-a', 'gpt-*'] });
  });

  it('normalizes the provider-specific dynamic model catalog', () => {
    expect(
      normalizeProviderModels({
        models: [{ id: 'dynamic-a' }, { id: 'dynamic-a' }, { id: 'dynamic-b' }],
      })
    ).toEqual([
      expect.objectContaining({ name: 'dynamic-a' }),
      expect.objectContaining({ name: 'dynamic-b' }),
    ]);
  });
  it('merges runtime plugin and static model catalogs without duplicates', () => {
    const runtime = normalizeProviderModels({
      models: [{ id: 'plugin-model', display_name: 'Plugin Model' }, { id: 'shared-model' }],
    });
    const staticModels = normalizeProviderModels({
      models: [{ id: 'SHARED-MODEL' }, { id: 'static-model' }],
    });

    expect(mergeProviderModelCatalogs(runtime, staticModels)).toEqual([
      expect.objectContaining({ name: 'plugin-model', alias: 'Plugin Model' }),
      expect.objectContaining({ name: 'shared-model' }),
      expect.objectContaining({ name: 'static-model' }),
    ]);
  });
});
