import { describe, expect, test } from 'bun:test';
import i18n from '@/i18n';
import {
  parseSearchKeyStatuses,
  parseSearchKeys,
  toSearchKeyPayload,
} from '@/services/api/searchKeys';
import {
  buildSearchKeyRows,
  buildSearchUsageSnippets,
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
        { provider: 'exa', 'api-key': 'exa-a', 'base-url': 'https://exa.example.com', disabled: true },
        { provider: 'tavily' },
        'junk',
      ],
    });

    expect(entries).toEqual([
      { provider: 'tavily', apiKey: 'tvly-a', label: 'acc1', baseUrl: '', proxyUrl: 'socks5://p:1', disabled: false },
      { provider: 'exa', apiKey: 'exa-a', label: '', baseUrl: 'https://exa.example.com', proxyUrl: '', disabled: true },
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
      },
    ]);
  });
});

describe('search key rows', () => {
  const entries = [
    { provider: 'tavily', apiKey: 'tvly-a', label: 'acc1', baseUrl: '', proxyUrl: '', disabled: false },
    { provider: 'tavily', apiKey: 'tvly-b', label: '', baseUrl: '', proxyUrl: '', disabled: false },
    { provider: 'exa', apiKey: 'exa-a', label: '', baseUrl: '', proxyUrl: '', disabled: true },
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
      { provider: 'tavily', total: 2, active: 1, cooling: 1, disabled: 0 },
      { provider: 'exa', total: 1, active: 0, cooling: 0, disabled: 1 },
      { provider: 'firecrawl', total: 0, active: 0, cooling: 0, disabled: 0 },
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
    const base = { provider: 'tavily', apiKey: 'k', label: '', baseUrl: '', proxyUrl: '', disabled: false };
    expect(validateSearchKeyForm(base, [])).toBeNull();
    expect(validateSearchKeyForm({ ...base, apiKey: '  ' }, [])).toBe('search_keys.error_key_required');
    expect(validateSearchKeyForm({ ...base, provider: 'bing' }, [])).toBe('search_keys.error_provider');
  });

  test('rejects a duplicate provider and key unless it is the entry being edited', () => {
    const existing = [{ provider: 'tavily', apiKey: 'k', label: '', baseUrl: '', proxyUrl: '', disabled: false }];
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
      for (const key of ['nav.search_keys', 'nav_meta.search_keys', 'search_keys.title', 'search_keys.usage_title']) {
        expect(i18n.exists(key, { lng: locale })).toBe(true);
      }
    }
  });
});
