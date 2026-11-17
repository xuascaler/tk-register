import { isIP } from 'node:net';
import { lookup as dnsLookup } from 'node:dns/promises';
import { fail } from './errors.mjs';

function ipv4Number(ip) {
  return ip.split('.').reduce((total, part) => total * 256 + Number(part), 0);
}
function v4In(ip, base, bits) {
  return Math.floor(ipv4Number(ip) / 2 ** (32 - bits)) === Math.floor(ipv4Number(base) / 2 ** (32 - bits));
}
function ipv6Number(ip) {
  let raw = ip.toLowerCase();
  if (raw.includes('.')) {
    const i = raw.lastIndexOf(':');
    const number = ipv4Number(raw.slice(i + 1));
    raw = raw.slice(0, i + 1) + Math.floor(number / 65536).toString(16) + ':' + (number % 65536).toString(16);
  }
  const [left, right] = raw.split('::');
  const a = left ? left.split(':') : [];
  const b = right ? right.split(':') : [];
  const parts = raw.includes('::') ? [...a, ...Array(8 - a.length - b.length).fill('0'), ...b] : a;
  return parts.reduce((n, part) => n * 65536n + BigInt('0x' + part), 0n);
}
function v6In(ip, base, bits) {
  return ipv6Number(ip) >> BigInt(128 - bits) === ipv6Number(base) >> BigInt(128 - bits);
}

/** Conservative global-unicast policy: reserved, transition and local ranges fail closed. */
export function isPublicIP(value) {
  const ip = String(value || '').replace(/^\[|\]$/g, '');
  const family = isIP(ip);
  if (family === 4) {
    return ![
      ['0.0.0.0', 8], ['10.0.0.0', 8], ['100.64.0.0', 10], ['127.0.0.0', 8],
      ['169.254.0.0', 16], ['172.16.0.0', 12], ['192.0.0.0', 24], ['192.0.2.0', 24],
      ['192.88.99.0', 24], ['192.168.0.0', 16], ['198.18.0.0', 15], ['198.51.100.0', 24],
      ['203.0.113.0', 24], ['224.0.0.0', 4], ['240.0.0.0', 4],
    ].some(([base, bits]) => v4In(ip, base, bits));
  }
  if (family === 6) {
    // This also excludes mapped IPv4, NAT64, ULA, link-local and multicast.
    return v6In(ip, '2000::', 3) && ![
      ['2001::', 23], ['2001:db8::', 32], ['2002::', 16], ['3fff::', 20],
    ].some(([base, bits]) => v6In(ip, base, bits));
  }
  return false;
}

export function parseProxy(value) {
  if (typeof value !== 'string' || !value.trim()) throw fail('proxy_required', 'Proxy required.');
  if (value.length > 4096 || /[\r\n\0]/.test(value)) throw fail('invalid_proxy', 'Invalid proxy.');
  let url;
  try { url = new URL(value.trim()); } catch { throw fail('invalid_proxy', 'Invalid proxy.'); }
  if (!['http:', 'https:', 'socks5:', 'socks5h:'].includes(url.protocol)
      || !url.hostname || (url.pathname && url.pathname !== '/') || url.search || url.hash) {
    throw fail('invalid_proxy', 'Invalid proxy.');
  }
  const hostname = url.hostname.replace(/^\[|\]$/g, '');
  if (hostname.includes('%')) throw fail('proxy_forbidden', 'Scoped addresses are forbidden.');
  let username, password;
  try { username = decodeURIComponent(url.username); password = decodeURIComponent(url.password); }
  catch { throw fail('invalid_proxy', 'Invalid proxy.'); }
  if ((!username && password) || /[\r\n\0]/.test(username + password)
      || Buffer.byteLength(username) > 255 || Buffer.byteLength(password) > 255) {
    throw fail('invalid_proxy', 'Invalid proxy credentials.');
  }
  const port = Number(url.port || (url.protocol === 'https:' ? 443 : url.protocol === 'http:' ? 80 : 1080));
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw fail('invalid_proxy', 'Invalid proxy port.');
  return { protocol: url.protocol, hostname, port, username, password };
}

export async function resolveProxy(value, { lookup = dnsLookup } = {}) {
  const proxy = parseProxy(value);
  let records;
  try {
    records = isIP(proxy.hostname)
      ? [{ address: proxy.hostname, family: isIP(proxy.hostname) }]
      : await lookup(proxy.hostname, { all: true, verbatim: true });
  } catch { throw fail('upstream_network_error', 'Proxy DNS lookup failed.', 502); }
  if (!Array.isArray(records) || !records.length || records.length > 16 || records.some(r => !isPublicIP(r.address))) {
    throw fail('proxy_forbidden', 'Proxy DNS must contain only public addresses.');
  }
  // The connector receives this IP, never the hostname: DNS rebinding cannot change its destination.
  return { ...proxy, address: records[0].address, family: isIP(records[0].address) };
}
