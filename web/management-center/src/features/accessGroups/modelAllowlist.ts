export const mergeModelNames = (...lists: ReadonlyArray<ReadonlyArray<string>>): string[] => {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const list of lists) {
    for (const item of list) {
      const name = item.trim();
      const key = name.toLowerCase();
      if (!name || seen.has(key)) continue;
      seen.add(key);
      result.push(name);
    }
  }
  return result;
};

export const filterModelNames = (models: ReadonlyArray<string>, query: string): string[] => {
  const needle = query.trim().toLowerCase();
  if (!needle) return [...models];
  return models.filter((model) => model.toLowerCase().includes(needle));
};
