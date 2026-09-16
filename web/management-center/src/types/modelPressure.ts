export interface ModelPressureRow {
  model: string;
  inFlight: number;
  requestsPerSecond: number;
  inputTokensPerSecond: number;
  outputTokensPerSecond: number;
  totalTokensPerSecond: number;
  failedPerSecond: number;
  errorRate: number;
  avgLatencyMs: number;
  avgTtftMs: number;
  servingAuths: number;
  suspendedAuths: number;
  quotaExceededAuths: number;
  providers: string[];
}

export interface ModelPressureSnapshot {
  windowSeconds: number;
  models: ModelPressureRow[];
}
