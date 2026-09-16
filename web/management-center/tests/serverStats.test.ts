import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { normalizeServerStats } from '../src/services/api/serverStats';

// Feature: disk and host monitoring on the server-monitor page
//
//   GET /v0/management/server-stats collects host gauges through gopsutil on
//   every supported platform: all physical disk partitions, swap, windowed
//   disk-IO and network rates. The page renders one gauge per mount, a disk
//   detail section, an IO throughput section, and a top alert when any disk
//   crosses 90% used.

describe('server stats API normalization', () => {
  test('normalizes disks rows from snake_case fields', () => {
    const stats = normalizeServerStats({
      uptime_seconds: 10,
      disks: [
        {
          mount: '/',
          device: '/dev/sda1',
          fstype: 'ext4',
          total_bytes: 500_000_000_000,
          avail_bytes: 50_000_000_000,
          used_percent: 90,
        },
        {
          mount: '/data',
          device: '/dev/sdb1',
          fstype: 'xfs',
          total_bytes: 1_000_000,
          avail_bytes: 900_000,
          used_percent: 10,
        },
      ],
    });
    expect(stats.disks).toEqual([
      {
        mount: '/',
        device: '/dev/sda1',
        fstype: 'ext4',
        totalBytes: 500_000_000_000,
        availBytes: 50_000_000_000,
        usedPercent: 90,
      },
      {
        mount: '/data',
        device: '/dev/sdb1',
        fstype: 'xfs',
        totalBytes: 1_000_000,
        availBytes: 900_000,
        usedPercent: 10,
      },
    ]);
  });

  test('normalizes host swap and windowed IO/network rates', () => {
    const stats = normalizeServerStats({
      host: {
        num_cpu: 8,
        mem_total_bytes: 1000,
        mem_available_bytes: 400,
        mem_used_percent: 60,
        swap_total_bytes: 2000,
        swap_free_bytes: 500,
        swap_used_percent: 75,
      },
      disk_io: { read_bytes_per_sec: 1200, write_bytes_per_sec: 3400 },
      network: {
        rx_bytes_per_sec: 5600,
        tx_bytes_per_sec: 7800,
        month: '2026-09',
        month_rx_bytes: 1_500_000_000,
        month_tx_bytes: 500_000_000,
        total_rx_bytes: 9_000_000_000,
        total_tx_bytes: 3_000_000_000,
      },
    });
    expect(stats.host).toMatchObject({
      memUsedPercent: 60,
      swapTotalBytes: 2000,
      swapFreeBytes: 500,
      swapUsedPercent: 75,
    });
    expect(stats.diskIo).toEqual({ readBytesPerSec: 1200, writeBytesPerSec: 3400 });
    expect(stats.network).toEqual({
      rxBytesPerSec: 5600,
      txBytesPerSec: 7800,
      month: '2026-09',
      monthRxBytes: 1_500_000_000,
      monthTxBytes: 500_000_000,
      totalRxBytes: 9_000_000_000,
      totalTxBytes: 3_000_000_000,
    });
  });

  test('leaves disks undefined when the backend omits the field', () => {
    const stats = normalizeServerStats({ uptime_seconds: 1 });
    expect(stats.disks).toBeUndefined();
    expect(stats.diskIo).toBeUndefined();
    expect(stats.network).toBeUndefined();
  });

  test('drops malformed disk rows fields to safe defaults', () => {
    const stats = normalizeServerStats({
      disks: [{ total_bytes: 'x', avail_bytes: -5, used_percent: 'y' }],
    });
    expect(stats.disks).toEqual([
      { mount: '/', device: '', fstype: '', totalBytes: 0, availBytes: 0, usedPercent: 0 },
    ]);
  });
});

describe('server monitor disk rendering contract', () => {
  const source = readFileSync(
    new URL('../src/features/serverMonitor/ServerMonitorPage.tsx', import.meta.url),
    'utf8'
  );

  test('renders one gauge per disk and a disk detail section', () => {
    expect(source).toContain('disks.map');
    expect(source).toContain('server_monitor.disk_section');
    expect(source).toContain('server_monitor.disk_label');
  });

  test('surfaces a >=90% used disk as a top alert independent of host metrics', () => {
    expect(source).toContain('disk_full_warning');
    expect(source).toContain('usedPercent >= 90');
    // disk detail section must not live inside the Linux-only host block
    const hostBlockEnd = source.indexOf('host_unavailable');
    const diskSection = source.indexOf('disk_section');
    expect(diskSection).toBeGreaterThan(hostBlockEnd);
  });

  test('renders windowed IO and network throughput from gopsutil counters', () => {
    expect(source).toContain('throughput_section');
    expect(source).toContain('stats.network');
    expect(source).toContain('stats.diskIo');
  });
});
