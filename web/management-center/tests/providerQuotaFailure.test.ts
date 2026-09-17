import { afterEach, describe, expect, test, spyOn } from 'bun:test';
import type { TFunction } from 'react-i18next';
import { OPENCODE_CONFIG } from '../src/features/quota/providers/providerApi/data';
import { providerQuotasApi } from '../src/services/api';
import type { AuthFileItem } from '../src/types';

// Feature: quota refresh failures must show the server's reason
//
//   The quota card used to report "the provider returned no quota data" for
//   every server-side refresh failure, hiding the actual cause (for example a
//   502 whose body names the misconfigured pool). These tests pin the error
//   propagation for the server-backed CommandCode/OpenCode providers.

const t = ((key: string) => key) as unknown as TFunction;
const file = { name: 'OpenCode Go #1 · ••••YQQC', type: 'opencode' } as unknown as AuthFileItem;

const spies: Array<{ mockRestore: () => void }> = [];
afterEach(() => {
  while (spies.length > 0) {
    spies.pop()?.mockRestore();
  }
});

describe('server-backed quota error propagation', () => {
  test('surfaces the refresh failure when no cached snapshot exists', async () => {
    spies.push(
      spyOn(providerQuotasApi, 'refresh').mockRejectedValue(
        new Error('provider quota: no configured credentials for opencode-go')
      )
    );
    spies.push(spyOn(providerQuotasApi, 'list').mockResolvedValue([]));

    await expect(OPENCODE_CONFIG.fetchQuota(file, t)).rejects.toThrow(
      'no configured credentials for opencode-go'
    );
  });

  test('prefers the cached snapshot last_error over the generic message', async () => {
    spies.push(spyOn(providerQuotasApi, 'refresh').mockRejectedValue(new Error('HTTP 502')));
    spies.push(
      spyOn(providerQuotasApi, 'list').mockResolvedValue([
        {
          provider: 'opencode-go',
          remaining_percent: 0,
          last_error: 'quota endpoint returned HTTP 401',
          sources: [],
        },
      ])
    );

    await expect(OPENCODE_CONFIG.fetchQuota(file, t)).rejects.toThrow(
      'quota endpoint returned HTTP 401'
    );
  });

  test('falls back to the refresh reason when the cached source has no windows', async () => {
    spies.push(
      spyOn(providerQuotasApi, 'refresh').mockRejectedValue(
        new Error('provider quota: no configured credentials for opencode-go')
      )
    );
    spies.push(
      spyOn(providerQuotasApi, 'list').mockResolvedValue([
        {
          provider: 'opencode-go',
          remaining_percent: 83,
          observed_at: '2026-09-17T08:55:36Z',
          sources: [
            {
              id: 'opencode-go-source',
              label: 'OpenCode Go #1',
              remaining_percent: 83,
              observed_at: '2026-09-17T08:55:36Z',
            },
          ],
        },
      ])
    );

    await expect(OPENCODE_CONFIG.fetchQuota(file, t)).rejects.toThrow(
      'no configured credentials for opencode-go'
    );
  });

  test('still reads a healthy server snapshot', async () => {
    spies.push(
      spyOn(providerQuotasApi, 'refresh').mockResolvedValue({
        provider: 'opencode-go',
        remaining_percent: 83,
        observed_at: '2026-09-17T09:04:33Z',
        sources: [
          {
            id: 'opencode-go-source',
            label: 'OpenCode Go #1',
            observed_at: '2026-09-17T09:04:33Z',
            quota: {
              groups: [
                {
                  display_name: 'OpenCode Go',
                  buckets: [
                    { window: 'rolling', remaining_fraction: 0.83 },
                    { window: 'weekly', remaining_fraction: 0.93 },
                  ],
                },
              ],
            },
          },
        ],
      })
    );

    const data = await OPENCODE_CONFIG.fetchQuota(file, t);
    expect(data.windows.map((window) => window.id)).toEqual(['rolling', 'weekly']);
    expect(data.windows[0].remainingPercent).toBe(83);
  });
});
