import type {
  ServerDiskStat,
  ServerIORate,
  ServerNetRate,
  ServerStats,
  ServerStatsHost,
} from '@/types';
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
    load1: asOptionalNumber(raw.load1),
    load5: asOptionalNumber(raw.load5),
    load15: asOptionalNumber(raw.load15),
    cpuPercent: asOptionalNumber(raw.cpu_percent ?? raw.cpuPercent),
    memTotalBytes: asNumber(raw.mem_total_bytes ?? raw.memTotalBytes),
    memAvailableBytes: asNumber(raw.mem_available_bytes ?? raw.memAvailableBytes),
    memUsedPercent: asOptionalNumber(raw.mem_used_percent ?? raw.memUsedPercent),
    swapTotalBytes: asOptionalNumber(raw.swap_total_bytes ?? raw.swapTotalBytes),
    swapFreeBytes: asOptionalNumber(raw.swap_free_bytes ?? raw.swapFreeBytes),
    swapUsedPercent: asOptionalNumber(raw.swap_used_percent ?? raw.swapUsedPercent),
    numCpu: asNumber(raw.num_cpu ?? raw.numCpu),
  };
};

const normalizeDisk = (value: unknown): ServerDiskStat => {
  const raw = asRecord(value);
  return {
    mount: typeof raw.mount === 'string' && raw.mount ? raw.mount : '/',
    device: typeof raw.device === 'string' ? raw.device : '',
    fstype: typeof raw.fstype === 'string' ? raw.fstype : '',
    totalBytes: asNumber(raw.total_bytes ?? raw.totalBytes),
    availBytes: asNumber(raw.avail_bytes ?? raw.availBytes),
    usedPercent: asNumber(raw.used_percent ?? raw.usedPercent),
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
    disks: Array.isArray(raw.disks) ? raw.disks.map(normalizeDisk) : undefined,
    diskIo: normalizeIoRate(raw.disk_io ?? raw.diskIo),
    network: normalizeNetRate(raw.network),
  };
};

const normalizeIoRate = (value: unknown): ServerIORate | undefined => {
  const raw = asRecord(value);
  if (Object.keys(raw).length === 0) return undefined;
  return {
    readBytesPerSec: asOptionalNumber(raw.read_bytes_per_sec ?? raw.readBytesPerSec),
    writeBytesPerSec: asOptionalNumber(raw.write_bytes_per_sec ?? raw.writeBytesPerSec),
  };
};

const normalizeNetRate = (value: unknown): ServerNetRate | undefined => {
  const raw = asRecord(value);
  if (Object.keys(raw).length === 0) return undefined;
  return {
    rxBytesPerSec: asOptionalNumber(raw.rx_bytes_per_sec ?? raw.rxBytesPerSec),
    txBytesPerSec: asOptionalNumber(raw.tx_bytes_per_sec ?? raw.txBytesPerSec),
    month: typeof raw.month === 'string' ? raw.month : undefined,
    monthRxBytes: asOptionalNumber(raw.month_rx_bytes ?? raw.monthRxBytes),
    monthTxBytes: asOptionalNumber(raw.month_tx_bytes ?? raw.monthTxBytes),
    totalRxBytes: asOptionalNumber(raw.total_rx_bytes ?? raw.totalRxBytes),
    totalTxBytes: asOptionalNumber(raw.total_tx_bytes ?? raw.totalTxBytes),
    source: typeof raw.source === 'string' ? raw.source : undefined,
  };
};

export const serverStatsApi = {
  getStats: async (): Promise<ServerStats> =>
    normalizeServerStats(await apiClient.get('/server-stats')),
};
