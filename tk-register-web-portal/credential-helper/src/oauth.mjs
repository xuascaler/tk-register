import { createHash, randomBytes } from 'node:crypto'
import { requestJson as defaultRequestJson } from './transport.mjs'
import { withStage, logDiagnostic } from './diagnostics.mjs'

const CLIENT_ID = '9d1c250a-e61b-44d9-88ed-5944d1962f5e'
const FULL_SCOPE = 'user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload'
const SESSION_TTL = 30 * 60 * 1000
const DEFAULT_ENDPOINTS = {
  authorizeUrl: 'https://claude.ai/oauth/authorize',
  tokenUrl: 'https://platform.claude.com/v1/oauth/token',
  redirectUri: 'https://platform.claude.com/oauth/code/callback',
  profileUrl: 'https://api.anthropic.com/api/oauth/profile',
  organizationsUrl: 'https://claude.ai/api/organizations',
  cookieAuthorizeBase: 'https://claude.ai/v1/oauth',
}

function fail(code, message, status = 400, extra = {}) {
  return Object.assign(new Error(message), { code, status, ...extra })
}

function text(...values) {
  return values.find(value => typeof value === 'string' && value.trim())?.trim() || ''
}

function identity(raw = {}) {
  const account = raw.account || raw.profile?.account || {}
  const organization = raw.organization || raw.profile?.organization || {}
  const oauthAccount = raw.oauth_account || {}
  return {
    email: text(raw.email, raw.email_address, account.email, account.email_address, oauthAccount.account_email),
    account_uuid: text(raw.account_uuid, raw.accountUuid, account.uuid, account.account_uuid, oauthAccount.account_uuid),
    organization_uuid: text(raw.organization_uuid, raw.org_uuid, organization.uuid, organization.organization_uuid, oauthAccount.organization_uuid),
    organization_name: text(raw.organization_name, organization.name, oauthAccount.organization_name),
  }
}

function mergeIdentity(credential, raw) {
  for (const [key, value] of Object.entries(identity(raw))) {
    if (value) credential[key] = value
  }
  return credential
}

function parseCode(input) {
  const raw = String(input || '').trim()
  if (!raw) throw fail('code_required', '请填写授权码或授权回调地址。')
  if (/^https?:\/\//i.test(raw)) {
    let url
    try { url = new URL(raw) } catch { throw fail('invalid_code', '授权回调地址格式不正确。') }
    if (url.searchParams.get('error')) throw fail('authorization_declined', '授权未完成，请重新生成链接并完成授权。')
    let code = url.searchParams.get('code')
    let state = url.searchParams.get('state') || ''
    if (!code && url.hash.startsWith('#code=')) {
      const fragment = new URLSearchParams(url.hash.slice(1))
      code = fragment.get('code')
      state = fragment.get('state') || state
    }
    if (!code) throw fail('invalid_code', '回调地址中没有找到授权码。')
    const parsed = parseCode(code)
    return { code: parsed.code, state: parsed.state || state }
  }
  const separator = raw.indexOf('#')
  return separator < 0
    ? { code: raw, state: '' }
    : { code: raw.slice(0, separator), state: raw.slice(separator + 1) }
}

function flow(mode = 'oauth', setupKind = 'full') {
  if (!['oauth', 'setup'].includes(mode)) throw fail('invalid_mode', '请选择 OAuth 或 Setup Token。')
  if (!['full', 'official'].includes(setupKind)) throw fail('invalid_setup_kind', 'Setup Token 模式不正确。')
  const official = mode === 'setup' && setupKind === 'official'
  return { official, scope: official ? 'user:inference' : FULL_SCOPE }
}

function pkce() {
  const verifier = randomBytes(32).toString('base64url')
  return {
    verifier,
    challenge: createHash('sha256').update(verifier).digest('base64url'),
    state: randomBytes(24).toString('base64url'),
  }
}

function requiredAccess(value) {
  const access = text(value)
  if (!access) throw fail('access_token_required', '请填写 access token。')
  return access
}

function requiredProxy(value) {
  if (typeof value !== 'string' || !value.trim()) throw fail('proxy_required', '请先绑定并验证代理。')
  return value.trim()
}

async function defaultVerifyProxy(proxyUrl) {
  requiredProxy(proxyUrl)
  return { ip: '' }
}

/** Protocol services are independent of VM state, databases and HTTP request objects. */
export function createOAuthService({ requestJson = defaultRequestJson, verifyProxy = defaultVerifyProxy, now = Date.now, endpoints = {} } = {}) {
  const urls = { ...DEFAULT_ENDPOINTS, ...endpoints }
  const sessions = new Map()
  const pendingGrants = new Map()
  const checkedProxy = (...args) => withStage('proxy_check', () => verifyProxy(...args))

  function grantKey(kind, secret, proxyUrl, extra = '') {
    return createHash('sha256').update(JSON.stringify([kind, secret, proxyUrl, extra])).digest('hex')
  }

  function authorizeParams(session) {
    return {
      response_type: 'code', client_id: CLIENT_ID, redirect_uri: urls.redirectUri,
      scope: session.scope, state: session.state,
      code_challenge: session.challenge, code_challenge_method: 'S256',
    }
  }

  function prune() {
    for (const [id, session] of sessions) {
      if (session.expiresAt <= now()) sessions.delete(id)
    }
    for (const [key, pending] of pendingGrants) {
      if (!pending.busy && pending.expiresAt <= now()) pendingGrants.delete(key)
    }
  }

  function takePending(key) {
    prune()
    let pending = pendingGrants.get(key)
    if (pending?.busy) throw fail('credential_busy', '这份凭证正在处理，请稍候。', 409)
    if (!pending) {
      if (pendingGrants.size >= 200) throw fail('too_many_sessions', '待完成的凭证操作过多，请稍后重试。', 429)
      pending = { credential: null, expiresAt: now() + SESSION_TTL, busy: false }
      pendingGrants.set(key, pending)
    }
    pending.busy = true
    return pending
  }

  function releasePending(key, pending) {
    pending.busy = false
    if (!pending.credential) pendingGrants.delete(key)
  }

  async function start({ mode = 'oauth', setup_kind = 'full', proxy_url = '', proxy_label = '已绑定代理' } = {}) {
    prune()
    if (sessions.size >= 200) throw fail('too_many_sessions', '待完成的授权过多，请稍后再试。', 429)
    const session = {
      ...flow(mode, setup_kind), ...pkce(), proxy_url: requiredProxy(proxy_url),
      proxy_label: String(proxy_label), expiresAt: now() + SESSION_TTL, busy: false,
    }
    const proxyCheck = await checkedProxy(session.proxy_url)
    session.exit_ip = text(proxyCheck?.ip)
    const id = randomBytes(24).toString('base64url')
    sessions.set(id, session)
    const url = new URL(urls.authorizeUrl)
    url.search = new URLSearchParams({ code: 'true', ...authorizeParams(session) }).toString()
    return {
      session_id: id, auth_url: url.href, expires_at: new Date(session.expiresAt).toISOString(),
      proxy_label: session.proxy_label,
      proxy_check: proxyCheck,
    }
  }

  async function enrich(credential, proxyUrl, notes) {
    try {
      await withStage('profile', async () => {
        const profile = await requestJson(urls.profileUrl, {
          proxy_url: proxyUrl,
          headers: { authorization: `Bearer ${credential.access_token}`, 'anthropic-beta': 'oauth-2025-04-20' },
        })
        mergeIdentity(credential, profile)
      })
    } catch (error) {
      if (['upstream_network_error', 'upstream_timeout', 'invalid_proxy', 'proxy_required', 'proxy_forbidden', 'proxy_check_failed', 'proxy_authentication_failed', 'proxy_ip_mismatch'].includes(error.code)) throw error
      logDiagnostic('warning', error)
      notes.push(error.upstreamStatus === 403
        ? 'Credentials were obtained, but the granted scope did not return complete account information.'
        : 'Credentials were obtained, but the profile query did not return complete account information.')
    }
    return credential
  }

  function fromToken(token, proxyUrl, previous = {}) {
    const access = text(token?.access_token, token?.accessToken)
    if (!access) throw fail('invalid_token_response', '上游响应中没有 access token。', 502)
    const credential = {
      ...previous,
      access_token: access,
      refresh_token: text(token.refresh_token, token.refreshToken, previous.refresh_token),
      id_token: text(token.id_token, previous.id_token),
      proxy_url: proxyUrl,
      last_refresh: new Date(now()).toISOString(),
      scope: text(token.scope, previous.scope),
    }
    // The old access token expiry is no longer authoritative after a refresh.
    delete credential.expired
    delete credential.expires_at
    const seconds = Number(token.expires_in)
    if (Number.isFinite(seconds) && seconds > 0) credential.expires_at = Math.floor(now() / 1000) + seconds
    else if (token.expires_at) credential.expires_at = token.expires_at
    return mergeIdentity(credential, token)
  }

  async function exchangeToken(session, parsed, { expectedState = '' } = {}) {
    return withStage('oauth_token', async () => {
      if (!parsed.code) throw fail('code_required', '授权码为空。')
      if (expectedState && parsed.state && parsed.state !== expectedState) {
        throw fail('state_mismatch', 'Cookie 授权返回的会话状态不匹配，请重新授权。')
      }
      const body = {
        grant_type: 'authorization_code', code: parsed.code, redirect_uri: urls.redirectUri,
        client_id: CLIENT_ID, code_verifier: session.verifier,
      }
      // Manual copy/paste callbacks may return a state different from the authorize nonce.
      // PKCE binds the code to this session. Send only the state actually returned upstream.
      if (parsed.state) body.state = parsed.state
      if (session.official) body.expires_in = 31536000
      const token = await requestJson(urls.tokenUrl, {
        method: 'POST', body, proxy_url: session.proxy_url,
      })
      const credential = fromToken(token, session.proxy_url)
      credential.proxy_exit_ip = session.exit_ip || ''
      if (!credential.scope) credential.scope = session.scope
      return credential
    })
  }

  async function finish(credential, proxyUrl, official) {
    const notes = []
    await enrich(credential, proxyUrl, notes)
    if (official) notes.push('Official Setup Token grants inference access only; refresh_token and account identity fields may be missing.')
    if (!credential.expires_at && !credential.expired) notes.push('The upstream did not provide an expiry. Supply the actual credential expiry before submission.')
    return { credential, notes }
  }

  async function exchange({ session_id, code } = {}) {
    prune()
    const session = sessions.get(String(session_id || ''))
    if (!session) throw fail('session_expired', '授权会话已过期或已完成，请重新生成链接。')
    if (session.busy) throw fail('session_busy', '该授权正在处理，请稍候。', 409)
    const parsed = session.credential ? null : parseCode(code)
    session.busy = true
    try {
      await checkedProxy(requiredProxy(session.proxy_url), session.exit_ip)
      if (!session.credential) session.credential = await exchangeToken(session, parsed)
      const result = await finish(session.credential, session.proxy_url, session.official)
      sessions.delete(session_id)
      return result
    } finally {
      session.busy = false
    }
  }

  async function cookie({ mode = 'oauth', setup_kind = 'full', session_key, proxy_url = '' } = {}) {
    const key = String(session_key || '').trim().replace(/^['"]|['"]$/g, '')
    if (!key.startsWith('sk-ant-sid')) throw fail('invalid_session_key', '请填写以 sk-ant-sid 开头的 sessionKey。')
    const session = { ...flow(mode, setup_kind), ...pkce(), proxy_url: requiredProxy(proxy_url) }
    const cacheKey = grantKey('cookie', key, session.proxy_url, `${mode}:${setup_kind}`)
    const pending = takePending(cacheKey)
    try {
      const proxyCheck = await checkedProxy(session.proxy_url, text(pending.credential?.proxy_exit_ip))
      session.exit_ip = text(proxyCheck?.ip)
      if (!pending.credential) {
        const webOrigin = new URL(urls.organizationsUrl).origin
        const headers = {
          cookie: `sessionKey=${key}`, origin: webOrigin, referer: `${webOrigin}/`,
          'user-agent': 'Mozilla/5.0',
        }
        const organization = await withStage('cookie_organizations', async () => {
          const organizations = await requestJson(urls.organizationsUrl, { headers, proxy_url: session.proxy_url })
          if (!Array.isArray(organizations) || !organizations.length) {
            throw fail('organization_missing', '此 sessionKey 没有返回可用组织。', 502)
          }
          const candidates = organizations.filter(org => org?.uuid && Array.isArray(org.capabilities) && org.capabilities.includes('chat'))
          const selected = candidates.sort((a, b) => b.capabilities.length - a.capabilities.length)[0]
            || organizations.find(org => org?.uuid)
          if (!selected) throw fail('organization_missing', '账号响应中没有组织 UUID。', 502)
          return selected
        })
        const callback = await withStage('cookie_authorize', async () => {
          const authorized = await requestJson(`${urls.cookieAuthorizeBase}/${encodeURIComponent(organization.uuid)}/authorize`, {
            method: 'POST', headers, proxy_url: session.proxy_url,
            body: { ...authorizeParams(session), organization_uuid: organization.uuid },
          })
          if (!authorized?.redirect_uri) throw fail('authorization_response_invalid', 'Cookie 授权没有返回授权回调地址。', 502)
          return parseCode(authorized.redirect_uri)
        })
        pending.credential = await exchangeToken(session, callback, { expectedState: session.state })
        if (!pending.credential.organization_uuid) pending.credential.organization_uuid = organization.uuid
        if (!pending.credential.organization_name && organization.name) pending.credential.organization_name = organization.name
      }
      const result = await finish(pending.credential, session.proxy_url, session.official)
      pendingGrants.delete(cacheKey)
      return result
    } finally {
      releasePending(cacheKey, pending)
    }
  }

  async function inspect({ access_token, refresh_token = '', expires_at, proxy_url = '' } = {}) {
    const credential = {
      access_token: requiredAccess(access_token), refresh_token: text(refresh_token),
      proxy_url: requiredProxy(proxy_url),
    }
    const proxyCheck = await checkedProxy(credential.proxy_url)
    credential.proxy_exit_ip = text(proxyCheck?.ip)
    if (expires_at) credential.expires_at = expires_at
    const notes = []
    await enrich(credential, credential.proxy_url, notes)
    if (!credential.expires_at) notes.push('The imported token has no expiry. Supply its actual expiry; no one-year validity is assumed.')
    notes.push('Importing a token does not refresh it; the actual last_refresh may be unknown.')
    return { credential, notes }
  }

  async function refresh({ auth, proxy_url } = {}) {
    if (!auth || typeof auth !== 'object' || Array.isArray(auth)) throw fail('auth_required', '请先导入或获取账号凭证。')
    const refreshToken = text(auth.refresh_token)
    if (!refreshToken) throw fail('refresh_token_required', '这份凭证没有 refresh_token，请重新授权获取凭证。')
    const proxyUrl = requiredProxy(proxy_url === undefined ? auth.proxy_url : proxy_url)
    const cacheKey = grantKey('refresh', refreshToken, proxyUrl)
    const pending = takePending(cacheKey)
    try {
      const proxyCheck = await checkedProxy(proxyUrl, text(pending.credential?.proxy_exit_ip, auth.proxy_exit_ip))
      if (!pending.credential) {
        const token = await withStage('oauth_token', () => requestJson(urls.tokenUrl, {
          method: 'POST', proxy_url: proxyUrl,
          body: { grant_type: 'refresh_token', refresh_token: refreshToken, client_id: CLIENT_ID },
        }))
        pending.credential = fromToken(token, proxyUrl, auth)
        pending.credential.proxy_exit_ip = text(proxyCheck?.ip)
      }
      const result = await finish(pending.credential, proxyUrl, false)
      pendingGrants.delete(cacheKey)
      return result
    } finally {
      releasePending(cacheKey, pending)
    }
  }

  // Internal retry metadata only; never export credentials from an unfinished operation.
  return { start, exchange, cookie, inspect, refresh,
    hasCredential: sessionID => Boolean(sessions.get(sessionID)?.credential) }
}
