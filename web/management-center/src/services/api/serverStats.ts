import type { ServerStats, ServerStatsHost } from '@/types';
import { apiClient } from './client';

const asRecord = (value: unknown): Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};

const asNumber = (value: unknown): number => {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? number : 0;
};

const asOptionalNumber = (value: unknown): number | undefined => {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? number : undefined;
};

const normalizeHost = (value: unknown): ServerStatsHost | undefined => {
  const raw = asRecord(value);
  if (!Object.keys(raw).length) return undefined;
  return {
    load1: asNumber(raw.load1),
    load5: asNumber(raw.load5),
    load15: asNumber(raw.load15),
    cpuPercent: asOptionalNumber(raw.cpu_percent ?? raw.cpuPercent),
    memTotalBytes: asNumber(raw.mem_total_bytes ?? raw.memTotalBytes),
    memAvailableBytes: asNumber(raw.mem_available_bytes ?? raw.memAvailableBytes),
    numCpu: asNumber(raw.num_cpu ?? raw.numCpu),
  };
};

export const normalizeServerStats = (value: unknown): ServerStats => {
  const raw = asRecord(value);
  return {
    uptimeSeconds: asNumber(raw.uptime_seconds ?? raw.uptimeSeconds),
    goroutines: asNumber(raw.goroutines),
    heapAllocBytes: asNumber(raw.heap_alloc_bytes ?? raw.heapAllocBytes),
    heapSysBytes: asNumber(raw.heap_sys_bytes ?? raw.heapSysBytes),
    stackInuseBytes: asNumber(raw.stack_inuse_bytes ?? raw.stackInuseBytes),
    gcCount: asNumber(raw.gc_count ?? raw.gcCount),
    numCpu: asNumber(raw.num_cpu ?? raw.numCpu),
    numFds: asOptionalNumber(raw.num_fds ?? raw.numFds),
    activeRequests: asNumber(raw.active_requests ?? raw.activeRequests),
    activeWebsockets: asNumber(raw.active_websockets ?? raw.activeWebsockets),
    processCpuSeconds: asOptionalNumber(raw.process_cpu_seconds ?? raw.processCpuSeconds),
    processCpuPercent: asOptionalNumber(raw.process_cpu_percent ?? raw.processCpuPercent),
    host: normalizeHost(raw.host),
  };
};

export const serverStatsApi = {
  getStats: async (): Promise<ServerStats> =>
    normalizeServerStats(await apiClient.get('/server-stats')),
};
