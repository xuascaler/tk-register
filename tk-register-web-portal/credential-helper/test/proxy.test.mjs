import test from 'node:test';
import assert from 'node:assert/strict';
import { isPublicIP, parseProxy, resolveProxy } from '../src/proxy.mjs';
import { allowedTarget, createTransport } from '../src/transport.mjs';

test('public proxy policy rejects local, metadata, reserved, mapped and transition addresses', () => {
  for (const ip of ['127.0.0.1', '0.0.0.0', '10.0.0.1', '100.100.100.200', '169.254.169.254',
    '172.16.0.1', '192.168.1.1', '198.18.0.1', '224.0.0.1', '255.255.255.255',
    '192.0.2.1', '203.0.113.1', '::1', '::ffff:127.0.0.1', '::ffff:8.8.8.8', 'fd00::1',
    'fe80::1', '64:ff9b::a00:1', '2002:7f00:1::', '2001:db8::1', '3fff::1', 'not-an-ip']) {
    assert.equal(isPublicIP(ip), false, ip);
  }
  for (const ip of ['8.8.8.8', '1.1.1.1', '2606:4700:4700::1111', '2001:4860:4860::8888']) {
    assert.equal(isPublicIP(ip), true, ip);
  }
});

test('all four proxy schemes parse safely and invalid credential/path URLs are rejected', () => {
  for (const scheme of ['http', 'https', 'socks5', 'socks5h']) {
    const p = parseProxy(`${scheme}://user:p%40ss@proxy.example:1080`);
    assert.equal(p.password, 'p@ss');
    assert.equal(p.hostname, 'proxy.example');
  }
  for (const value of ['file:///tmp/x', 'http://proxy.example/a', 'http://proxy.example?target=x',
    'http://proxy.example/#x', 'http://u:%0D%0Ax@proxy.example', 'http://:secret@proxy.example', 'direct']) {
    assert.throws(() => parseProxy(value));
  }
});

test('DNS mixed answers fail closed; public answer is returned as a pinned address', async () => {
  await assert.rejects(resolveProxy('http://proxy.example', { lookup: async () => [
    { address: '8.8.8.8', family: 4 }, { address: '10.1.1.1', family: 4 },
  ] }), { code: 'proxy_forbidden' });
  const result = await resolveProxy('https://proxy.example', { lookup: async () => [{ address: '8.8.8.8', family: 4 }] });
  assert.equal(result.address, '8.8.8.8');
  assert.equal(result.hostname, 'proxy.example');
  await assert.rejects(resolveProxy('http://2130706433'), { code: 'proxy_forbidden' });
  await assert.rejects(resolveProxy('http://0x7f000001'), { code: 'proxy_forbidden' });
});

test('transport allowlist forbids arbitrary upstream URLs and redirects', () => {
  for (const value of ['http://api.ipify.org/?format=json', 'https://evil.example/',
    'https://platform.claude.com/else', 'https://user:pass@api.anthropic.com/api/oauth/profile',
    'https://claude.ai/v1/oauth/../../admin/authorize', 'https://api.ipify.org:8443/?format=json']) {
    assert.throws(() => allowedTarget(value));
  }
  assert.equal(allowedTarget('https://claude.ai/v1/oauth/org-uuid/authorize').hostname, 'claude.ai');
});

test('private DNS is rejected before dial and failed public dial never falls back', async () => {
  let dials = 0;
  const privateTransport = createTransport({ lookup: async () => [{ address: '169.254.169.254' }], dial: () => { dials++; } });
  await assert.rejects(privateTransport('https://api.ipify.org/?format=json', { proxy_url: 'http://proxy.example' }), { code: 'proxy_forbidden' });
  assert.equal(dials, 0);
  const publicTransport = createTransport({ lookup: async () => [{ address: '8.8.8.8' }], dial: async options => {
    dials++; assert.equal(options.host, '8.8.8.8'); throw new Error('secret-proxy-password');
  } });
  await assert.rejects(publicTransport('https://api.ipify.org/?format=json', { proxy_url: 'http://proxy.example' }), error => {
    assert.equal(error.code, 'upstream_network_error'); assert.ok(!error.message.includes('secret')); return true;
  });
  assert.equal(dials, 1);
});
