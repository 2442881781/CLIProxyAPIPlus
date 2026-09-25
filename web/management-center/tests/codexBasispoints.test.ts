import { describe, expect, test } from 'bun:test';
import { parse as parseYaml } from 'yaml';
import {
  applyAuthFileBasispoints,
  readAuthFileBasispoints,
  supportsAuthFileBasispoints,
} from '@/features/authFiles/constants';
import {
  buildAuthFileFieldsPatch,
  type PrefixProxyEditorState,
} from '@/features/authFiles/hooks/useAuthFilesPrefixProxyEditor';
import { runVisualConfig } from './helpers/visualConfig';

const makeEditor = (
  json: Record<string, unknown>,
  basispoints: boolean,
  basispointsTouched: boolean
): PrefixProxyEditorState => ({
  fileName: 'codex.json',
  fileInfoText: '',
  loading: false,
  saving: false,
  error: null,
  originalText: JSON.stringify(json),
  rawText: JSON.stringify(json),
  invalidContentPreview: '',
  json,
  providerKey: 'codex',
  prefix: '',
  proxyUrl: '',
  priority: '',
  weight: '',
  weightError: null,
  disableCooling: false,
  disableCoolingTouched: false,
  websockets: false,
  websocketsTouched: false,
  usingApi: false,
  usingApiTouched: false,
  basispoints,
  basispointsTouched,
  note: '',
  noteTouched: false,
  excludedModelsText: '',
  excludedModelsTouched: false,
  headersText: '',
  headersTouched: false,
  headersError: null,
});

describe('visual Codex Basispoints config', () => {
  const yaml = `codex:
  future-option: keep
  basispoints:
    enabled: true
    base-url: https://bps.example.test/responses
    models:
      - gpt-5.6-sol
      - gpt-6-astra
    native-fallback: true
`;

  test('loads the global configuration', () => {
    const config = runVisualConfig(yaml);
    expect(config.visualValues.codexBasispointsEnabled).toBe(true);
    expect(config.visualValues.codexBasispointsBaseUrl).toBe(
      'https://bps.example.test/responses'
    );
    expect(config.visualValues.codexBasispointsModels).toEqual(['gpt-5.6-sol', 'gpt-6-astra']);
    expect(config.visualValues.codexBasispointsNativeFallback).toBe(true);
  });

  test('writes normalized values and preserves unknown Codex settings', () => {
    const config = runVisualConfig(yaml, [
      {
        codexBasispointsEnabled: false,
        codexBasispointsBaseUrl: ' https://proxy.example.test/bps ',
        codexBasispointsModels: [' gpt-5.6-sol ', '', 'all'],
        codexBasispointsNativeFallback: false,
      },
    ]);

    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      codex: {
        'future-option': 'keep',
        basispoints: {
          enabled: false,
          'base-url': ' https://proxy.example.test/bps ',
          models: ['gpt-5.6-sol', 'all'],
          'native-fallback': false,
        },
      },
    });
  });
});

describe('Codex auth-file Basispoints opt-in', () => {
  test('is exposed only for Codex credentials and reads boolean-compatible values', () => {
    expect(supportsAuthFileBasispoints('codex')).toBe(true);
    expect(supportsAuthFileBasispoints('claude')).toBe(false);
    expect(readAuthFileBasispoints({ basispoints: true })).toBe(true);
    expect(readAuthFileBasispoints({ basispoints: 'true' })).toBe(true);
    expect(readAuthFileBasispoints({})).toBe(false);
  });

  test('writes an explicit boolean only after the switch is touched', () => {
    expect(buildAuthFileFieldsPatch(makeEditor({}, true, false), (key) => key)).toEqual({});
    expect(buildAuthFileFieldsPatch(makeEditor({}, true, true), (key) => key)).toEqual({
      basispoints: true,
    });
    expect(
      buildAuthFileFieldsPatch(makeEditor({ basispoints: true }, false, true), (key) => key)
    ).toEqual({ basispoints: false });
    expect(applyAuthFileBasispoints({ type: 'codex' }, true)).toEqual({
      type: 'codex',
      basispoints: true,
    });
  });
});
