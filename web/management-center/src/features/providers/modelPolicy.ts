import type { OAuthModelAliasEntry } from '@/types';

export interface ModelPolicy {
  allowedModels: string[];
  aliases: OAuthModelAliasEntry[];
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const readString = (value: unknown): string =>
  typeof value === 'string' ? value.trim() : '';

export const normalizeModelNames = (values: Iterable<unknown>): string[] => {
  const seen = new Set<string>();
  const result: string[] = [];
  Array.from(values).forEach((value) => {
    const trimmed = readString(value);
    if (!trimmed) return;
    const key = trimmed.toLowerCase();
    if (seen.has(key)) return;
    seen.add(key);
    result.push(trimmed);
  });
  return result;
};

export const readModelPolicyAliases = (value: unknown): OAuthModelAliasEntry[] => {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  return value.flatMap((item) => {
    if (!isRecord(item)) return [];
    const name = readString(item.name ?? item.model ?? item.id);
    const alias = readString(item.alias);
    if (!name || !alias || name.toLowerCase() === alias.toLowerCase()) return [];
    const aliasKey = alias.toLowerCase();
    if (seen.has(aliasKey)) return [];
    seen.add(aliasKey);
    const entry: OAuthModelAliasEntry = { name, alias };
    if (item.fork === true) entry.fork = true;
    const forceMapping = item['force-mapping'] ?? item.forceMapping;
    if (typeof forceMapping === 'boolean') entry.forceMapping = forceMapping;
    return [entry];
  });
};

export const normalizeModelPolicy = (policy?: Partial<ModelPolicy> | null): ModelPolicy => ({
  allowedModels: normalizeModelNames(policy?.allowedModels ?? []),
  aliases: readModelPolicyAliases(policy?.aliases ?? []),
});

export const modelPolicySignature = (policy: ModelPolicy): string => {
  const normalized = normalizeModelPolicy(policy);
  return JSON.stringify({
    allowedModels: normalized.allowedModels.map((item) => item.toLowerCase()).sort(),
    aliases: normalized.aliases
      .map((entry) => ({
        name: entry.name.trim(),
        alias: entry.alias.trim(),
        fork: entry.fork === true,
        forceMapping: entry.forceMapping === true,
      }))
      .sort((left, right) => left.alias.localeCompare(right.alias, undefined, { sensitivity: 'base' })),
  });
};
