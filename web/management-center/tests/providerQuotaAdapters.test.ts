import { describe, expect, test } from 'bun:test';
import type { TFunction } from 'i18next';
import { apiCallApi } from '../src/services/api/apiCall';
import {
  buildCommandCodeQuotaFiles,
  buildOpenCodeQuotaFiles,
  buildZhipuQuotaFiles,
  readProviderQuotaCredential,
} from '../src/features/quota/providerQuotaSources';
import {
  providerQuotaParsers,
  ZHIPU_CONFIG,
} from '../src/features/quota/providers/providerApi/data';

const t = ((key: string) => key) as TFunction;

describe('AI provider quota sources', () => {
  test('creates one masked CommandCode quota item per weighted key without exposing the key in its name', () => {
    const files = buildCommandCodeQuotaFiles({
      enabled: true,
      api_keys: [
        { key: 'user-secret-alpha', weight: 10, proxy_url: 'http://proxy.local' },
        { key: 'user-secret-beta', weight: 20 },
      ],
    });

    expect(files).toHaveLength(2);
    expect(files[0]?.name).not.toContain('user-secret');
    expect(files[0]?.type).toBe('commandcode');
    expect(readProviderQuotaCredential(files[0]!)).toEqual({
      apiKey: 'user-secret-alpha',
      proxyUrl: 'http://proxy.local',
    });
  });

  test('uses the configured OpenCode base URL and marks disabled plugins unavailable', () => {
    const files = buildOpenCodeQuotaFiles({
      enabled: false,
      'base-url': 'https://example.test/go/v1',
      'api-keys': [{ value: 'oc-secret' }],
    });

    expect(files[0]?.disabled).toBe(true);
    expect(readProviderQuotaCredential(files[0]!)).toEqual({
      apiKey: 'oc-secret',
      baseUrl: 'https://example.test/go/v1',
    });
  });

  test('recognizes only Zhipu Coding Plan compatibility entries and preserves per-key proxies', () => {
    const files = buildZhipuQuotaFiles([
      {
        name: 'Other',
        baseUrl: 'https://example.test/v1',
        apiKeyEntries: [{ apiKey: 'skip-me' }],
      },
      {
        name: 'Zhipu GLM Coding Plan',
        baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
        apiKeyEntries: [{ apiKey: 'glm-secret', proxyUrl: 'socks5://proxy.local' }],
      },
    ]);

    expect(files).toHaveLength(1);
    expect(readProviderQuotaCredential(files[0]!)).toEqual({
      apiKey: 'glm-secret',
      baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
      proxyUrl: 'socks5://proxy.local',
      providerName: 'Zhipu GLM Coding Plan',
      planKind: 'personal',
    });
  });

  test('classifies custom-named Team Plan credentials and normalizes team headers', () => {
    const files = buildZhipuQuotaFiles([
      {
        name: '团队版',
        baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
        apiKeyEntries: [{ apiKey: 'team-secret' }],
        headers: {
          'Bigmodel-Organization': 'org-1',
          'bigmodel-project': 'project-1',
        },
      },
    ]);

    expect(files[0]?.name).toContain('团队版');
    expect(files[0]?.name).not.toContain('team-secret');
    expect(readProviderQuotaCredential(files[0]!)).toEqual({
      apiKey: 'team-secret',
      baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
      providerName: '团队版',
      planKind: 'team',
      headers: {
        'bigmodel-organization': 'org-1',
        'bigmodel-project': 'project-1',
      },
    });
  });
});

describe('Zhipu quota requests', () => {
  test('uses the Team endpoint and organization/project headers', async () => {
    const file = buildZhipuQuotaFiles([
      {
        name: 'Team Plan',
        baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
        apiKeyEntries: [{ apiKey: 'team-secret' }],
        headers: {
          'bigmodel-organization': 'org-1',
          'bigmodel-project': 'project-1',
        },
      },
    ])[0]!;
    const originalRequest = apiCallApi.request;
    const requests: Parameters<typeof apiCallApi.request>[0][] = [];
    apiCallApi.request = async (payload) => {
      requests.push(payload);
      return {
        statusCode: 200,
        header: {},
        bodyText: '',
        body: payload.url.includes('customer-package-reset')
          ? { success: true, code: 200, data: { fiveHourResets: [], weekResets: [] } }
          : {
              success: true,
              data: {
                limits: [{ type: 'TOKENS_LIMIT', unit: 6, number: 1, percentage: 5 }],
              },
            },
      };
    };

    try {
      await ZHIPU_CONFIG.fetchQuota(file, t);
    } finally {
      apiCallApi.request = originalRequest;
    }

    expect(requests.map((request) => request.url).sort()).toEqual([
      'https://open.bigmodel.cn/api/biz/customer-package-reset/list?targetType=TEAM',
      'https://open.bigmodel.cn/api/monitor/usage/quota/limit?type=2',
    ]);
    requests.forEach((request) => {
      expect(request.header).toMatchObject({
        Authorization: 'Bearer team-secret',
        'bigmodel-organization': 'org-1',
        'bigmodel-project': 'project-1',
      });
    });
  });

  test('uses a selected personal reset credit and refreshes quota without exposing the key', async () => {
    const file = buildZhipuQuotaFiles([
      {
        name: 'Personal Plan',
        baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
        apiKeyEntries: [{ apiKey: 'personal-secret' }],
      },
    ])[0]!;
    const originalRequest = apiCallApi.request;
    const requests: Parameters<typeof apiCallApi.request>[0][] = [];
    apiCallApi.request = async (payload) => {
      requests.push(payload);
      const isResetEndpoint = payload.url.includes('customer-package-reset');
      return {
        statusCode: 200,
        header: {},
        bodyText: '',
        body:
          payload.method === 'POST'
            ? { success: true, code: 200 }
            : isResetEndpoint
              ? { success: true, code: 200, data: { fiveHourResets: [], weekResets: [] } }
              : {
                  success: true,
                  data: {
                    limits: [{ type: 'TOKENS_LIMIT', unit: 6, number: 1, percentage: 25 }],
                  },
                },
      };
    };

    try {
      await ZHIPU_CONFIG.resetQuota?.(file, t, 'WEEK:230451');
    } finally {
      apiCallApi.request = originalRequest;
    }

    const request = requests.find((item) => item.method === 'POST');
    expect(request?.url).toBe('https://open.bigmodel.cn/api/biz/customer-package-reset/use');
    expect(request?.header).toMatchObject({
      Authorization: 'Bearer personal-secret',
      'Content-Type': 'application/json',
    });
    expect(JSON.parse(request?.data ?? '{}')).toMatchObject({
      targetType: 'PERSONAL',
      resetType: 'WEEK',
      recordId: 230451,
    });
    expect(typeof JSON.parse(request?.data ?? '{}').requestId).toBe('string');
  });

  test('offers the earliest available reset only when its quota has usage', () => {
    const actions = ZHIPU_CONFIG.getResetActions?.(
      {
        status: 'success',
        windows: [
          { id: 'session', label: 'session', usedPercent: 0, remainingPercent: 100 },
          { id: 'weekly', label: 'weekly', usedPercent: 57, remainingPercent: 43 },
        ],
        resetCredits: [
          {
            actionId: 'WEEK:earlier',
            type: 'WEEK',
            recordId: 'earlier',
            expiresAtMs: 100,
            available: true,
          },
          {
            actionId: 'WEEK:later',
            type: 'WEEK',
            recordId: 'later',
            expiresAtMs: 200,
            available: true,
          },
          {
            actionId: 'FIVE_HOUR:unused',
            type: 'FIVE_HOUR',
            recordId: 'unused',
            expiresAtMs: 50,
            available: true,
          },
        ],
      },
      t
    );

    expect(actions?.map((action) => action.id)).toEqual(['WEEK:earlier']);
  });

  test('explains which Team identifiers are missing before making a request', async () => {
    const file = buildZhipuQuotaFiles([
      {
        name: '团队版',
        baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4',
        apiKeyEntries: [{ apiKey: 'team-secret' }],
      },
    ])[0]!;

    await expect(ZHIPU_CONFIG.fetchQuota(file, t)).rejects.toThrow(
      'provider_quota.zhipu_team_ids_required'
    );
  });
});

describe('AI provider quota payload adapters', () => {
  test('maps CommandCode 5-hour/weekly limits and monthly USD credits', () => {
    const quota = providerQuotaParsers.commandcode(
      {
        credits: { monthlyCredits: 69.99 },
        windowLimits: {
          fiveHour: { used: 2, cap: 10, resetAt: 1_800_000_000_000 },
          weekly: { used: 25, cap: 100, resetAt: 1_800_100_000_000 },
        },
      },
      t
    );

    expect(quota.balance).toBe(69.99);
    expect(quota.windows.map((window) => window.remainingPercent)).toEqual([80, 75]);
  });

  test('maps OpenCode usage percentages as used quota', () => {
    const quota = providerQuotaParsers.opencode(
      {
        usage: {
          rolling: { percent: 9, resetsAt: '2026-09-05T12:20:04Z' },
          weekly: { percent: 12, resetsAt: '2026-09-10T00:00:00Z' },
          monthly: { percent: 6, resetsAt: '2026-10-01T00:00:00Z' },
        },
      },
      t
    );

    expect(quota.windows.map((window) => window.remainingPercent)).toEqual([91, 88, 94]);
    expect(quota.windows.every((window) => Number.isFinite(window.resetAtMs))).toBe(true);
  });

  test('maps Zhipu token, credit, and MCP quota windows using remaining values', () => {
    const quota = providerQuotaParsers.zhipu(
      {
        data: {
          limits: [
            { type: 'CREDIT_LIMIT', unit: 3, number: 5, usage: 100, remaining: 83 },
            { type: 'TOKENS_LIMIT', unit: 6, number: 1, percentage: 42 },
            { type: 'TIME_LIMIT', currentValue: 8, usage: 1000, remaining: 992 },
          ],
        },
      },
      t
    );

    expect(quota.windows.map((window) => window.id)).toEqual(['session', 'weekly', 'mcp']);
    expect(quota.windows[2]?.remainingPercent).toBe(99.2);
  });

  test('maps Zhipu reset credits, availability, and Beijing-time expiry', () => {
    const credits = providerQuotaParsers.zhipuResetCredits(
      {
        success: true,
        code: 200,
        data: {
          fiveHourResets: [{ recordId: 100, expireTime: '2026-09-13 04:11:02', available: false }],
          weekResets: [{ recordId: 200, expireTime: '2026-10-01 23:59:59', available: true }],
        },
      },
      t
    );

    expect(credits.map((credit) => credit.actionId)).toEqual(['WEEK:200', 'FIVE_HOUR:100']);
    expect(credits[0]).toMatchObject({
      type: 'WEEK',
      recordId: '200',
      available: true,
      expiresAtMs: Date.parse('2026-10-01T23:59:59+08:00'),
    });
  });

  test('surfaces a Zhipu business error returned with HTTP 200', () => {
    expect(() =>
      providerQuotaParsers.zhipu({ success: false, code: 500, msg: '当前用户不存在coding plan' }, t)
    ).toThrow('当前用户不存在coding plan');
  });

  test('maps Devin passive quota signals refreshed by the backend', () => {
    const quota = providerQuotaParsers.devin(
      {
        name: 'devin.json',
        type: 'devin',
        quota: {
          signals: {
            plan: 'Team',
            daily_quota_remaining_percent: '70%',
            weekly_quota_remaining_percent: '40%',
            daily_quota_reset_at: '1800000000000',
          },
        },
      },
      t
    );

    expect(quota.plan).toBe('Team');
    expect(quota.windows.map((window) => window.usedPercent)).toEqual([30, 60]);
  });
});
