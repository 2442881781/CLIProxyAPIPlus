import { describe, expect, test } from 'bun:test';
import {
  buildCommandCodeModelsPatch,
  readCommandCodeModels,
} from '../src/features/providers/commandCodeModels';

describe('CommandCode model allowlist', () => {
  test('shows plugin defaults when no explicit model list is configured', () => {
    const result = readCommandCodeModels({});

    expect(result.usesDefaults).toBe(true);
    expect(result.models).toEqual([
      {
        alias: 'deepseek-flash',
        name: 'deepseek/deepseek-v4.1-flash',
        displayName: '',
      },
      {
        alias: 'glm-5.3-flash',
        name: 'z-ai/glm-5.3-flash',
        displayName: '',
      },
    ]);
  });

  test('reads explicit structured and legacy scalar entries without exposing other config', () => {
    const result = readCommandCodeModels({
      api_keys: [{ key: 'secret' }],
      models: [
        {
          alias: 'deepseek-only',
          name: 'deepseek/deepseek-v4.1-flash',
          display_name: 'DeepSeek V4.1',
        },
        'legacy-model',
      ],
    });

    expect(result).toEqual({
      usesDefaults: false,
      models: [
        {
          alias: 'deepseek-only',
          name: 'deepseek/deepseek-v4.1-flash',
          displayName: 'DeepSeek V4.1',
        },
        { alias: 'legacy-model', name: '', displayName: '' },
      ],
    });
  });

  test('builds a one-model allowlist and trims every persisted field', () => {
    expect(
      buildCommandCodeModelsPatch(
        [
          {
            alias: ' deepseek-flash ',
            name: ' deepseek/deepseek-v4.1-flash ',
            displayName: ' DeepSeek V4.1 Flash ',
          },
        ],
        false
      )
    ).toEqual({
      models: [
        {
          alias: 'deepseek-flash',
          name: 'deepseek/deepseek-v4.1-flash',
          display_name: 'DeepSeek V4.1 Flash',
        },
      ],
    });
  });

  test('deletes the models key when restoring plugin defaults', () => {
    expect(buildCommandCodeModelsPatch([], true)).toEqual({ models: null });
  });
});
