import { APIError } from '../api';
import { parseAuthJSON } from '../submissions/validation';
import { emailDiffers, readProxyCheck, type CredentialFlow } from '../submissions/credential-flow';

export type GeneratedAuthJSON = Omit<CredentialFlow, 'status' | 'auth'> & {
  status: 'exported'; auth: Record<string, unknown>; generated_at: string; audit_id?: string;
};

// A successful HTTP response is not enough to display or export credentials.
// Confirm the bound flow, account identity and the same complete JSON contract
// used by the existing authorization UI before exposing a downloadable result.
export function readGeneratedAuthJSON(value: unknown, flowID: string, accountEmail: string, expectedProxy?: { ip: string; url: string }): GeneratedAuthJSON {
  if (!value || typeof value !== 'object') throw new Error('The generated Auth JSON could not be confirmed.');
  const result = value as Partial<GeneratedAuthJSON>;
  if (result.flow_id !== flowID || result.status !== 'exported' || !result.auth || typeof result.auth !== 'object' || Array.isArray(result.auth) || typeof result.generated_at !== 'string' || !result.generated_at || !Number.isFinite(Date.parse(result.generated_at)) || typeof result.expires_at !== 'string' || !Number.isFinite(Date.parse(result.expires_at))) throw new Error('The generated Auth JSON could not be confirmed.');
  const auth = parseAuthJSON(JSON.stringify(result.auth));
  if (emailDiffers(accountEmail, auth.email)) throw new Error('The authorized email does not match this account.');
  const proxy = readProxyCheck(result.proxy_check);
  if (expectedProxy && (proxy.ip !== expectedProxy.ip || auth.proxy_url !== expectedProxy.url)) throw new Error('The generated credentials do not match this authorization’s proxy.');
  return { ...result, auth } as GeneratedAuthJSON;
}

export function generationResultUncertain(cause: unknown) {
  return cause instanceof APIError && (cause.status === 0 || cause.status >= 500 || cause.status === 200 || cause.code === 'CREDENTIAL_GENERATION_UNCONFIRMED');
}
