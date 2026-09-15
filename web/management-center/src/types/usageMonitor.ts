export interface UsageMonitorTokenTotals {
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  unclassifiedTokens: number;
  totalTokens: number;
}

export interface UsageMonitorModelStats {
  model: string;
  requests: number;
  success: number;
  failed: number;
  averageLatencyMs: number;
  lastUsedAt: string;
  tokens: UsageMonitorTokenTotals;
}

export interface UsageMonitorProviderStats {
  provider: string;
  requests: number;
  success: number;
  failed: number;
  averageLatencyMs: number;
  lastUsedAt: string;
  tokens: UsageMonitorTokenTotals;
  models: UsageMonitorModelStats[];
}

export interface UsageMonitorSnapshot {
  enabled: boolean;
  since: string;
  updatedAt?: string;
  truncated: boolean;
  requests: number;
  success: number;
  failed: number;
  tokens: UsageMonitorTokenTotals;
  providers: UsageMonitorProviderStats[];
}
