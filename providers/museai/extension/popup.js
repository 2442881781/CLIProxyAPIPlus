'use strict';
(() => {
  const $ = id => document.getElementById(id);
  let active = null;
  let submitting = false;

  function status(message, error = false) {
    $('status').textContent = message;
    $('status').classList.toggle('error', error);
  }

  function destination(raw) {
    let url;
    try { url = new URL(raw); } catch (_) { throw new Error('Enter a valid CPA origin.'); }
    if (url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
      throw new Error('CPA must be an origin only: no credentials, path, query, or fragment.');
    }
    const loopback = url.hostname === 'localhost' || url.hostname === '127.0.0.1';
    if (url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback)) {
      throw new Error('Use HTTPS, or HTTP on localhost or 127.0.0.1 only.');
    }
    return {
      endpoint: url.origin + '/v8/management/plugins/museai/accounts',
      // Chrome host permissions are host-scoped, not port-scoped. The actual fetch
      // below stays pinned to the exact user-selected origin and rejects redirects.
      permission: url.protocol + '//' + url.hostname + '/*'
    };
  }

  async function submit(target, permissionPromise, secret, metadata) {
    let records = [];
    let cookies = Object.create(null);
    let expiries = Object.create(null);
    let serialized = '';
    try {
      const granted = await permissionPromise;
      if (!granted) { status('CPA host permission was declined. No cookies were read or sent.', true); return; }
      status('Reading Muse.ai cookies in memory…');
      // Domain filtering includes every path on muse.ai, unlike a document.cookie
      // read. chrome.cookies includes HttpOnly values and true expirationDate.
      records = await chrome.cookies.getAll({domain: 'muse.ai'});
      const now = Date.now() / 1000;
      for (const record of records) {
        if (record.domain.replace(/^\./, '') !== 'muse.ai' || !record.name) continue;
        if (!record.session && Number.isFinite(record.expirationDate) && record.expirationDate <= now) continue;
        // The CPA wire contract is a name:value map, so incompatible duplicate
        // names cannot be represented without silently discarding a credential.
        if (Object.prototype.hasOwnProperty.call(cookies, record.name)) {
          if (cookies[record.name] !== record.value) {
            status('Muse.ai has conflicting cookies with the same name on different paths. The CPA cookie map cannot represent them; nothing was sent. Resolve the conflicting browser cookies before importing.', true);
            return;
          }
          if (!record.session && Number.isFinite(record.expirationDate)) {
            const expiry = Math.floor(record.expirationDate);
            if (expiries[record.name] === undefined || expiry < expiries[record.name]) expiries[record.name] = expiry;
          }
          continue;
        }
        cookies[record.name] = record.value;
        if (!record.session && Number.isFinite(record.expirationDate)) expiries[record.name] = Math.floor(record.expirationDate);
      }
      const count = Object.keys(cookies).length;
      if (!count) { status('No unexpired Muse.ai cookies were found. Sign in to https://muse.ai in this Chrome profile, then reopen the popup.', true); return; }
      serialized = JSON.stringify({...metadata, cookies, cookies_exp: expiries});
      status('Sending credentials directly to your selected CPA origin…');
      active = new AbortController();
      const response = await fetch(target.endpoint, {
        method: 'POST',
        headers: {'Authorization':'Bearer ' + secret, 'Content-Type':'application/json', 'Accept':'application/json'},
        body: serialized,
        credentials: 'omit',
        cache: 'no-store',
        redirect: 'error',
        referrerPolicy: 'no-referrer',
        signal: active.signal
      });
      // Never read/render a response body: an upstream or server error may echo
      // submitted credentials. Only status and local cookie count are displayed.
      if (!response.ok) {
        const message = response.status === 401 || response.status === 403
          ? 'CPA rejected management authentication. Enter a valid key and try again.'
          : 'CPA import failed (HTTP ' + response.status + '). Check the server logs; credentials are not displayed here.';
        status(message, true); return;
      }
      const type = response.headers.get('Content-Type') || '';
      if (response.status !== 204 && !/(?:application\/json|\+json)(?:;|$)/i.test(type)) {
        status('CPA returned a non-JSON response. Check your manager before retrying; credentials may already have been saved.', true); return;
      }
      status('Import accepted by CPA (' + count + ' cookies). Verify the account in the Muse.ai manager. No Muse.ai probe was requested.');
    } catch (_) {
      // Do not echo exception strings, endpoints, payloads, key, or cookie values.
      status('Import did not complete. Check CPA connectivity and the account list before retrying; the server may already have received the credentials. Your key was cleared.', true);
    } finally {
      secret = ''; serialized = '';
      for (const record of records) record.value = '';
      records.length = 0;
      for (const name of Object.keys(cookies)) delete cookies[name];
      for (const name of Object.keys(expiries)) delete expiries[name];
      cookies = null; expiries = null;
      if (metadata.proxy_url) metadata.proxy_url = '';
      active = null; submitting = false; $('import').disabled = false;
      $('key').value = ''; $('proxy').value = '';
    }
  }

  $('import-form').addEventListener('submit', event => {
    event.preventDefault();
    if (submitting) return;
    let secret = $('key').value.trim();
    $('key').value = '';
    const proxy = $('proxy').value.trim(); $('proxy').value = '';
    if (!secret) { status('Enter the CPA management key.', true); return; }
    let target;
    try { target = destination($('origin').value.trim()); }
    catch (error) { secret = ''; status(error.message, true); return; }
    const metadata = {label: $('label').value.trim()};
    const name = $('name').value.trim();
    if (name) metadata.name = name;
    if (proxy) metadata.proxy_url = proxy;
    submitting = true; $('import').disabled = true;
    // This call must happen synchronously in the button/form user gesture,
    // before cookie reads, fetches, or any awaited work.
    let permissionPromise;
    try { permissionPromise = chrome.permissions.request({origins:[target.permission]}); }
    catch (_) { secret = ''; submitting = false; $('import').disabled = false; status('Chrome could not request the selected host permission.', true); return; }
    void submit(target, permissionPromise, secret, metadata);
    secret = '';
  });

  window.addEventListener('pagehide', () => {
    $('key').value = ''; $('proxy').value = '';
    if (active) active.abort();
  });
})();
