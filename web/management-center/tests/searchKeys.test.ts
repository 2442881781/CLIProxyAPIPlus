import { describe, expect, test } from 'bun:test';
import i18n from '@/i18n';
import {
  parseSearchKeyStatuses,
  parseSearchKeys,
  toSearchKeyPayload,
  type SearchKeyStatus,
} from '@/services/api/searchKeys';
import {
  buildSearchKeyRows,
  buildSearchUsageSnippets,
  describeQuota,
  formatCooldown,
  summarizeSearchProviders,
  validateSearchKeyForm,
} from '@/features/searchKeys/searchKeysModel';

const LOCALES = ['en', 'zh-CN', 'zh-TW', 'ru'];
const NOW = Date.parse('2026-09-23T00:00:00Z');

describe('search key API mapping', () => {
  test('parses entries from the management payload and drops malformed rows', () => {
    const entries = parseSearchKeys({
      'search-api-key': [
        { provider: 'tavily', 'api-key': 'tvly-a', label: 'acc1', 'proxy-url': 'socks5://p:1' },
        {
          provider: 'exa',
          'api-key': 'exa-a',
          'base-url': 'https://exa.example.com',
          disabled: true,
        },
        { provider: 'tavily' },
        'junk',
      ],
    });

    expect(entries).toEqual([
      {
        provider: 'tavily',
        apiKey: 'tvly-a',
        label: 'acc1',
        baseUrl: '',
        proxyUrl: 'socks5://p:1',
        disabled: false,
        budget: 0,
      },
      {
        provider: 'exa',
        apiKey: 'exa-a',
        label: '',
        baseUrl: 'https://exa.example.com',
        proxyUrl: '',
        disabled: true,
        budget: 0,
      },
    ]);
  });

  test('serializes entries with trimmed values and omits empty optionals', () => {
    expect(
      toSearchKeyPayload({
        provider: 'firecrawl',
        apiKey: ' fc-1 ',
        label: ' ',
        baseUrl: '',
        proxyUrl: ' http://proxy:8080 ',
        disabled: false,
        budget: 0,
      })
    ).toEqual({ provider: 'firecrawl', 'api-key': 'fc-1', 'proxy-url': 'http://proxy:8080' });
  });

  test('parses runtime status with config index', () => {
    const statuses = parseSearchKeyStatuses({
      keys: [
        {
          id: 'tavily:abc',
          index: 1,
          provider: 'tavily',
          'masked-key': 'tvly****cdef',
          'cooldown-until': '2026-09-23T01:00:00Z',
          'last-status': 432,
          requests: 5,
          failures: 2,
        },
      ],
    });

    expect(statuses).toEqual([
      {
        id: 'tavily:abc',
        index: 1,
        provider: 'tavily',
        maskedKey: 'tvly****cdef',
        cooldownUntil: Date.parse('2026-09-23T01:00:00Z'),
        lastStatus: 432,
        requests: 5,
        failures: 2,
        quota: null,
        exhausted: false,
      },
    ]);
  });
});

describe('search key rows', () => {
  const entries = [
    {
      provider: 'tavily',
      apiKey: 'tvly-a',
      label: 'acc1',
      baseUrl: '',
      proxyUrl: '',
      disabled: false,
      budget: 0,
    },
    {
      provider: 'tavily',
      apiKey: 'tvly-b',
      label: '',
      baseUrl: '',
      proxyUrl: '',
      disabled: false,
      budget: 0,
    },
    {
      provider: 'exa',
      apiKey: 'exa-a',
      label: '',
      baseUrl: '',
      proxyUrl: '',
      disabled: true,
      budget: 0,
    },
  ] as const;
  const statuses = [
    {
      id: 'tavily:b',
      index: 1,
      provider: 'tavily',
      maskedKey: 'tvly****-b',
      cooldownUntil: NOW + 90_000,
      lastStatus: 429,
      requests: 3,
      failures: 1,
      quota: null,
      exhausted: false,
    },
  ];

  test('joins status by config index and derives state', () => {
    const rows = buildSearchKeyRows([...entries], statuses, NOW);

    expect(rows.map((row) => [row.index, row.state, row.cooldownRemainingMs])).toEqual([
      [0, 'active', 0],
      [1, 'cooling', 90_000],
      [2, 'disabled', 0],
    ]);
    expect(rows[1].status?.requests).toBe(3);
    expect(rows[0].status).toBeNull();
  });

  test('expired cooldown counts as active', () => {
    const rows = buildSearchKeyRows([...entries], statuses, NOW + 120_000);
    expect(rows[1].state).toBe('active');
  });

  test('summarizes every provider even without keys', () => {
    const summary = summarizeSearchProviders(buildSearchKeyRows([...entries], statuses, NOW));

    expect(summary).toEqual([
      {
        provider: 'tavily',
        total: 2,
        active: 1,
        cooling: 1,
        disabled: 0,
        exhausted: 0,
        remaining: null,
        unit: 'credits',
      },
      {
        provider: 'exa',
        total: 1,
        active: 0,
        cooling: 0,
        disabled: 1,
        exhausted: 0,
        remaining: null,
        unit: 'usd',
      },
      {
        provider: 'firecrawl',
        total: 0,
        active: 0,
        cooling: 0,
        disabled: 0,
        exhausted: 0,
        remaining: null,
        unit: 'credits',
      },
    ]);
  });
});

describe('cooldown formatting', () => {
  test('uses the two largest units', () => {
    expect(formatCooldown(45_000)).toBe('45s');
    expect(formatCooldown(90_000)).toBe('1m 30s');
    expect(formatCooldown(6 * 3_600_000 - 60_000)).toBe('5h 59m');
    expect(formatCooldown(25 * 3_600_000)).toBe('1d 1h');
  });
});

describe('search key form', () => {
  test('requires a known provider and a non-empty key', () => {
    const base = {
      provider: 'tavily',
      apiKey: 'k',
      label: '',
      baseUrl: '',
      proxyUrl: '',
      disabled: false,
      budget: 0,
    };
    expect(validateSearchKeyForm(base, [])).toBeNull();
    expect(validateSearchKeyForm({ ...base, apiKey: '  ' }, [])).toBe(
      'search_keys.error_key_required'
    );
    expect(validateSearchKeyForm({ ...base, provider: 'bing' }, [])).toBe(
      'search_keys.error_provider'
    );
  });

  test('rejects a duplicate provider and key unless it is the entry being edited', () => {
    const existing = [
      {
        provider: 'tavily',
        apiKey: 'k',
        label: '',
        baseUrl: '',
        proxyUrl: '',
        disabled: false,
        budget: 0,
      },
    ];
    const form = { ...existing[0], label: 'x' };
    expect(validateSearchKeyForm(form, existing)).toBe('search_keys.error_duplicate');
    expect(validateSearchKeyForm(form, existing, 0)).toBeNull();
  });
});

describe('search usage snippets', () => {
  test('builds REST base URLs, MCP URL and client commands from the API base', () => {
    const snippets = buildSearchUsageSnippets('https://cpa.example.com/');

    expect(snippets.restBase).toEqual({
      tavily: 'https://cpa.example.com/search/tavily',
      exa: 'https://cpa.example.com/search/exa',
      firecrawl: 'https://cpa.example.com/search/firecrawl',
    });
    expect(snippets.mcpUrl).toBe('https://cpa.example.com/search/mcp');
    expect(snippets.claudeMcpCommand).toBe(
      'claude mcp add --transport http cpa-search https://cpa.example.com/search/mcp --header "Authorization: Bearer <CPA_API_KEY>"'
    );
    expect(snippets.firecrawlMcpEnv).toBe(
      'FIRECRAWL_API_URL=https://cpa.example.com/search/firecrawl FIRECRAWL_API_KEY=<CPA_API_KEY>'
    );
  });
});

describe('search keys i18n', () => {
  test('every locale defines the page strings', () => {
    for (const locale of LOCALES) {
      for (const key of [
        'nav.search_keys',
        'nav_meta.search_keys',
        'search_keys.title',
        'search_keys.usage_title',
        'search_keys.col_quota',
        'search_keys.refresh_quota',
        'search_keys.state_exhausted',
        'search_keys.field_budget',
        'search_keys.reset_spend',
      ]) {
        expect(i18n.exists(key, { lng: locale })).toBe(true);
      }
    }
  });
});

// Feature: quota display
describe('search key quota', () => {
  const baseEntry = {
    provider: 'tavily',
    apiKey: 'k',
    label: '',
    baseUrl: '',
    proxyUrl: '',
    disabled: false,
    budget: 0,
  };
  const status = (index: number, patch: Partial<SearchKeyStatus>): SearchKeyStatus => ({
    id: `id-${index}`,
    index,
    provider: 'tavily',
    maskedKey: '****',
    cooldownUntil: 0,
    lastStatus: 0,
    requests: 0,
    failures: 0,
    quota: null,
    exhausted: false,
    ...patch,
  });

  test('parses quota fields from status', () => {
    const [parsed] = parseSearchKeyStatuses({
      keys: [
        {
          id: 'tavily:a',
          index: 0,
          provider: 'tavily',
          exhausted: true,
          quota: {
            unit: 'credits',
            used: 1000,
            limit: 1000,
            remaining: 0,
            plan: 'Free',
            'reset-at': '2026-10-01T00:00:00Z',
            'checked-at': '2026-09-23T00:00:00Z',
            error: 'boom',
          },
        },
        { id: 'exa:b', index: 1, provider: 'exa', quota: { unit: 'usd', used: 3.21 } },
      ],
    });
    expect(parsed.exhausted).toBe(true);
    expect(parsed.quota).toEqual({
      unit: 'credits',
      used: 1000,
      limit: 1000,
      remaining: 0,
      plan: 'Free',
      resetAt: Date.parse('2026-10-01T00:00:00Z'),
      checkedAt: Date.parse('2026-09-23T00:00:00Z'),
      error: 'boom',
    });
    const exa = parseSearchKeyStatuses({
      keys: [{ index: 1, quota: { unit: 'usd', used: 3.21 } }],
    })[0];
    expect(exa.quota?.limit).toBeNull();
    expect(exa.quota?.remaining).toBeNull();
    expect(parseSearchKeyStatuses({ keys: [{ index: 0 }] })[0].quota).toBeNull();
  });

  test('formats credits and usd quota cells', () => {
    const credits = describeQuota({
      unit: 'credits',
      used: 150,
      limit: 1000,
      remaining: 850,
      plan: '',
      resetAt: 0,
      checkedAt: 0,
      error: '',
    });
    expect(credits).toEqual({ text: '850 / 1,000', percentRemaining: 85, spentOnly: false });
    const usd = describeQuota({
      unit: 'usd',
      used: 9.98,
      limit: 10,
      remaining: 0.02,
      plan: '',
      resetAt: 0,
      checkedAt: 0,
      error: '',
    });
    expect(usd).toEqual({ text: '$0.02 / $10.00', percentRemaining: 0.2, spentOnly: false });
    expect(describeQuota(null)).toBeNull();
    expect(
      describeQuota({
        unit: '',
        used: null,
        limit: null,
        remaining: null,
        plan: '',
        resetAt: 0,
        checkedAt: 0,
        error: 'x',
      })
    ).toBeNull();
  });

  test('formats exa spend without budget', () => {
    expect(
      describeQuota({
        unit: 'usd',
        used: 3.214,
        limit: null,
        remaining: null,
        plan: '',
        resetAt: 0,
        checkedAt: 0,
        error: '',
      })
    ).toEqual({ text: '$3.21', percentRemaining: null, spentOnly: true });
  });

  test('sums remaining quota per provider', () => {
    const entries = [
      { ...baseEntry, apiKey: 'a' },
      { ...baseEntry, apiKey: 'b' },
      { ...baseEntry, apiKey: 'c', disabled: true },
      { ...baseEntry, provider: 'exa', apiKey: 'e', budget: 10 },
    ];
    const quota = (remaining: number, unit = 'credits') => ({
      unit,
      used: 0,
      limit: 1000,
      remaining,
      plan: '',
      resetAt: 0,
      checkedAt: 0,
      error: '',
    });
    const rows = buildSearchKeyRows(
      entries,
      [
        status(0, { quota: quota(850) }),
        status(1, { quota: quota(100) }),
        status(2, { quota: quota(999) }),
        status(3, { provider: 'exa', quota: quota(9.5, 'usd') }),
      ],
      NOW
    );
    const summary = summarizeSearchProviders(rows);
    expect(summary.map((item) => [item.provider, item.remaining, item.unit])).toEqual([
      ['tavily', 950, 'credits'],
      ['exa', 9.5, 'usd'],
      ['firecrawl', null, 'credits'],
    ]);
  });

  test('marks exhausted keys', () => {
    const rows = buildSearchKeyRows(
      [
        { ...baseEntry, apiKey: 'a' },
        { ...baseEntry, apiKey: 'b' },
      ],
      [status(0, { exhausted: true }), status(1, {})],
      NOW
    );
    expect(rows.map((row) => row.state)).toEqual(['exhausted', 'active']);
    const [tavily] = summarizeSearchProviders(rows);
    expect([tavily.active, tavily.exhausted]).toEqual([1, 1]);
  });

  test('serializes budget only when positive', () => {
    expect(toSearchKeyPayload({ ...baseEntry, provider: 'exa', budget: 10 }).budget).toBe(10);
    expect('budget' in toSearchKeyPayload({ ...baseEntry, budget: 0 })).toBe(false);
    expect(
      parseSearchKeys({ 'search-api-key': [{ provider: 'exa', 'api-key': 'e', budget: 12.5 }] })[0]
        .budget
    ).toBe(12.5);
  });
});
