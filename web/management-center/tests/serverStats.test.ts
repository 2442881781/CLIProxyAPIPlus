import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { normalizeServerStats } from '../src/services/api/serverStats';

// Feature: disk monitoring on the server-monitor page
//
//   GET /v0/management/server-stats returns a `disks` array (mount, total_bytes,
//   avail_bytes, used_percent) deduplicated by device on the backend. The page
//   renders one gauge per mount, a detail section, and a top alert when any
//   disk crosses 90% used — disks are monitored on every platform, not just
//   Linux where host-level metrics exist.

describe('server stats API normalization', () => {
  test('normalizes disks rows from snake_case fields', () => {
    const stats = normalizeServerStats({
      uptime_seconds: 10,
      disks: [
        { mount: '/', total_bytes: 500_000_000_000, avail_bytes: 50_000_000_000, used_percent: 90 },
        { mount: '/data', total_bytes: 1_000_000, avail_bytes: 900_000, used_percent: 10 },
      ],
    });
    expect(stats.disks).toEqual([
      { mount: '/', totalBytes: 500_000_000_000, availBytes: 50_000_000_000, usedPercent: 90 },
      { mount: '/data', totalBytes: 1_000_000, availBytes: 900_000, usedPercent: 10 },
    ]);
  });

  test('leaves disks undefined when the backend omits the field', () => {
    const stats = normalizeServerStats({ uptime_seconds: 1 });
    expect(stats.disks).toBeUndefined();
  });

  test('drops malformed disk rows fields to safe defaults', () => {
    const stats = normalizeServerStats({
      disks: [{ total_bytes: 'x', avail_bytes: -5, used_percent: 'y' }],
    });
    expect(stats.disks).toEqual([{ mount: '/', totalBytes: 0, availBytes: 0, usedPercent: 0 }]);
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
});
