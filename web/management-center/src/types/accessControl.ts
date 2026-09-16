export interface RateLimitSpec {
  rpm: number;
  tpm: number;
  rpd: number;
  maxConcurrency: number;
}

export interface AccessGroupUsageTotals {
  tokens: number;
  requests: number;
  failed: number;
  lastUsedAt: string;
}

export interface AccessGroup {
  name: string;
  allowedAuths: string[];
  allowedModels: string[];
  maxConcurrency: number;
  rateLimitRpm: number;
  perKeyLimits: RateLimitSpec;
  usage: AccessGroupUsageTotals;
  createdAt: string;
  updatedAt: string;
}

export interface AccessAuthItem {
  id: string;
  label: string;
  provider: string;
  prefix: string;
  file: string;
  disabled: boolean;
}

export interface UsageDimRow {
  name: string;
  tokens: number;
  requests: number;
  failed: number;
  lastUsedAt: string;
}

export interface AccessGroupUsageDetail {
  name: string;
  keys: number;
  totals: AccessGroupUsageTotals;
  avgLatencyMs: number;
  avgTtftMs: number;
  models: UsageDimRow[];
  daily: UsageDimRow[];
  auths: UsageDimRow[];
}

export type AccessKeyUsageTopBy = 'tokens' | 'requests' | 'failed';
export type AccessKeyUsagePeriod = 'all' | 'month' | 'day';

export interface AccessKeyUsageTopRow {
  id: string;
  name: string;
  keyPrefix: string;
  group: string;
  tokens: number;
  requests: number;
  failed: number;
  lastUsedAt: string;
}

export interface AccessKeyUsageDetail {
  id: string;
  name: string;
  keyPrefix: string;
  group: string;
  totalTokens: number;
  periodTokens: number;
  requests: number;
  failed: number;
  lastUsedAt: string;
  models: UsageDimRow[];
  daily: UsageDimRow[];
  auths: UsageDimRow[];
}

export interface ServerStatsHost {
  load1?: number;
  load5?: number;
  load15?: number;
  cpuPercent?: number;
  memTotalBytes: number;
  memAvailableBytes: number;
  memUsedPercent?: number;
  swapTotalBytes?: number;
  swapFreeBytes?: number;
  swapUsedPercent?: number;
  numCpu: number;
}

export interface ServerDiskStat {
  mount: string;
  device: string;
  fstype: string;
  totalBytes: number;
  availBytes: number;
  usedPercent: number;
}

export interface ServerIORate {
  readBytesPerSec?: number;
  writeBytesPerSec?: number;
}

export interface ServerNetRate {
  rxBytesPerSec?: number;
  txBytesPerSec?: number;
}

export interface ServerStats {
  uptimeSeconds: number;
  goroutines: number;
  heapAllocBytes: number;
  heapSysBytes: number;
  stackInuseBytes: number;
  gcCount: number;
  numCpu: number;
  numFds?: number;
  activeRequests: number;
  activeWebsockets: number;
  processCpuSeconds?: number;
  processCpuPercent?: number;
  host?: ServerStatsHost;
  disks?: ServerDiskStat[];
  diskIo?: ServerIORate;
  network?: ServerNetRate;
}
