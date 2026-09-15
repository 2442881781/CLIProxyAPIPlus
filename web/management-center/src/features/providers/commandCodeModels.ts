import type { PluginConfigObject } from '@/types';

export interface CommandCodeModelEntry {
  alias: string;
  name: string;
  displayName: string;
}

export const COMMANDCODE_DEFAULT_MODELS: readonly CommandCodeModelEntry[] = [
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
];

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const readString = (value: unknown): string => (typeof value === 'string' ? value.trim() : '');

const cloneDefaultModels = (): CommandCodeModelEntry[] =>
  COMMANDCODE_DEFAULT_MODELS.map((entry) => ({ ...entry }));

export const readCommandCodeModels = (
  config: PluginConfigObject
): { models: CommandCodeModelEntry[]; usesDefaults: boolean } => {
  if (!Array.isArray(config.models) || config.models.length === 0) {
    return { models: cloneDefaultModels(), usesDefaults: true };
  }

  const models = config.models.flatMap((value) => {
    if (typeof value === 'string') {
      const alias = value.trim();
      return alias ? [{ alias, name: '', displayName: '' }] : [];
    }
    if (!isRecord(value)) return [];
    const alias = readString(value.alias);
    const name = readString(value.name);
    if (!alias && !name) return [];
    return [
      {
        alias,
        name,
        displayName: readString(value.display_name ?? value.displayName),
      },
    ];
  });

  return models.length > 0
    ? { models, usesDefaults: false }
    : { models: cloneDefaultModels(), usesDefaults: true };
};

export const buildCommandCodeModelsPatch = (
  models: CommandCodeModelEntry[],
  usesDefaults: boolean
): PluginConfigObject => {
  if (usesDefaults) return { models: null };
  return {
    models: models.map((model) => ({
      alias: model.alias.trim(),
      name: model.name.trim(),
      ...(model.displayName.trim() ? { display_name: model.displayName.trim() } : {}),
    })),
  };
};
