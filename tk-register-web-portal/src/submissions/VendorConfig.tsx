import { Check, LockKeyhole, ShieldCheck } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, request } from "../api";
import { ErrorNotice, Loading } from "../ui";
import { dateTime } from "./shared";
import type { Vendor } from "./types";
import VendorSelect from "./VendorSelect";

export default function VendorConfig({
  onDirty,
}: {
  onDirty: (dirty: boolean) => void;
}) {
  const [vendors, setVendors] = useState<Vendor[] | null>(null);
  const [selected, setSelected] = useState("");
  const [vendor, setVendor] = useState<Vendor | null>(null);
  const [token, setToken] = useState("");
  const [enabled, setEnabled] = useState(false);
  const [timezone, setTimezone] = useState("UTC");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [attempt, setAttempt] = useState(0);
  const dirty = Boolean(
    vendor &&
    (token || enabled !== vendor.enabled || timezone !== vendor.timezone),
  );
  useEffect(() => {
    onDirty(dirty);
    return () => onDirty(false);
  }, [dirty, onDirty]);
  useEffect(() => {
    const abort = new AbortController();
    setError("");
    setLoading(true);
    request<{ vendors: Vendor[] }>("/vendors", { signal: abort.signal })
      .then(({ vendors: values }) => {
        setVendors(values);
        setSelected((current) => current || values[0]?.id || "");
        if (!values.length) setLoading(false);
      })
      .catch((cause) => {
        if (!abort.signal.aborted) {
          setError(errorMessage(cause));
          setLoading(false);
        }
      });
    return () => abort.abort();
  }, [attempt]);
  useEffect(() => {
    if (!selected) return;
    const abort = new AbortController();
    setLoading(true);
    setError("");
    request<Vendor>(`/admin/vendors/${encodeURIComponent(selected)}`, {
      signal: abort.signal,
    })
      .then((result) => {
        setVendor(result);
        setEnabled(result.enabled);
        setTimezone(result.timezone);
        setToken("");
      })
      .catch((cause) => {
        if (!abort.signal.aborted) setError(errorMessage(cause));
      })
      .finally(() => {
        if (!abort.signal.aborted) setLoading(false);
      });
    return () => abort.abort();
  }, [selected, attempt]);
  async function save(event: FormEvent) {
    event.preventDefault();
    if (!vendor || busy || !dirty) return;
    setError("");
    setNotice("");
    try {
      new Intl.DateTimeFormat("en", { timeZone: timezone });
    } catch {
      setError("Enter a valid IANA timezone, such as Asia/Shanghai or UTC.");
      return;
    }
    if (token && (/\s/.test(token) || token.startsWith("Bearer "))) {
      setError("Enter the token only, without “Bearer” or spaces.");
      return;
    }
    if (enabled && !vendor.configured && !token) {
      setError("Add a platform token before enabling submissions.");
      return;
    }
    setBusy(true);
    try {
      const result = await request<Vendor>(
        `/admin/vendors/${encodeURIComponent(vendor.id)}`,
        {
          method: "PUT",
          body: {
            enabled,
            timezone,
            ...(token ? { access_token: token } : {}),
          },
        },
      );
      setVendor(result);
      setEnabled(result.enabled);
      setTimezone(result.timezone);
      setToken("");
      setNotice("Vendor configuration saved.");
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="vendor-config">
      <ErrorNotice>{error}</ErrorNotice>
      {loading ? (
        <Loading label="Loading vendor configuration…" />
      ) : !vendors || !vendor ? (
        <>
          {!error ? (
            <p className="muted">No vendors have been configured.</p>
          ) : (
            <button
              className="button secondary"
              onClick={() => setAttempt((n) => n + 1)}
            >
              Try again
            </button>
          )}
        </>
      ) : (
        <>
          <div className="vendor-selection">
            <div className="field">
              <VendorSelect
                id="config-vendor"
                label="Vendor"
                value={selected}
                disabled={busy}
                options={vendors.map((item) => ({
                  value: item.id,
                  label: item.name,
                }))}
                onChange={(value) => {
                  if (
                    dirty &&
                    !window.confirm("Discard your unsaved vendor changes?")
                  )
                    return;
                  setSelected(value);
                  setNotice("");
                }}
              />
            </div>
            <span
              className={`credential-state ${vendor.configured ? "configured" : ""}`}
            >
              {vendor.configured ? (
                <ShieldCheck aria-hidden="true" />
              ) : (
                <LockKeyhole aria-hidden="true" />
              )}
              {vendor.configured
                ? "Credential configured"
                : "Credential required"}
            </span>
          </div>
          <form onSubmit={save} noValidate autoComplete="off">
            <table className="config-table vendor-table">
              <thead>
                <tr>
                  <th scope="col">Key</th>
                  <th scope="col">Value</th>
                  <th scope="col">Description</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>
                    <code>enabled</code>
                  </td>
                  <td>
                    <button
                      type="button"
                      role="switch"
                      aria-checked={enabled}
                      aria-label="Enable vendor submissions"
                      className={`switch ${enabled ? "on" : ""}`}
                      disabled={busy}
                      onClick={() => setEnabled(!enabled)}
                    >
                      <span />
                    </button>
                    <span className="switch-label">
                      {enabled ? "Enabled" : "Disabled"}
                    </span>
                  </td>
                  <td>
                    <p>
                      Allow account submissions and queries for this vendor.
                    </p>
                  </td>
                </tr>
                <tr>
                  <td>
                    <code>access_token</code>
                    <span className="secret-label">
                      <LockKeyhole aria-hidden="true" />
                      Secret
                    </span>
                  </td>
                  <td>
                    <label className="sr-only" htmlFor="vendor-token">
                      Vendor access token
                    </label>
                    <input
                      id="vendor-token"
                      name="vendor-token"
                      type="password"
                      autoComplete="new-password"
                      value={token}
                      disabled={busy}
                      onChange={(e) => setToken(e.target.value)}
                      placeholder={
                        vendor.configured
                          ? "Enter a replacement token"
                          : "Enter platform Bearer token"
                      }
                    />
                    <p className="field-help">
                      {vendor.configured
                        ? "Leave blank to keep the existing token."
                        : "Provided by the platform administrator."}
                    </p>
                  </td>
                  <td>
                    <p>
                      Shared by this vendor’s protocols. Encrypted in storage;
                      the saved value cannot be viewed.
                    </p>
                    {vendor.credential_updated_at && (
                      <p className="field-help">
                        Updated {dateTime(vendor.credential_updated_at)}
                      </p>
                    )}
                  </td>
                </tr>
                <tr>
                  <td>
                    <code>timezone</code>
                  </td>
                  <td>
                    <label className="sr-only" htmlFor="vendor-timezone">
                      Vendor timezone
                    </label>
                    <input
                      id="vendor-timezone"
                      value={timezone}
                      disabled={busy}
                      onChange={(e) => setTimezone(e.target.value)}
                      placeholder="Asia/Shanghai"
                      autoCapitalize="none"
                      spellCheck={false}
                    />
                  </td>
                  <td>
                    <p>
                      Timezone used for platform activity and billing queries.
                    </p>
                  </td>
                </tr>
              </tbody>
            </table>
            <div className="vendor-protocols">
              <h2>Protocols</h2>
              <ul>
                {vendor.protocols.map((protocol) => (
                  <li key={protocol.version_id}>
                    <span>{protocol.name}</span>
                    <span className="muted small">
                      {protocol.operation === "push" ? "Submit" : "Query"} · v
                      {protocol.version}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
            {notice && (
              <div className="notice success" role="status">
                <Check aria-hidden="true" />
                {notice}
              </div>
            )}
            <div className="save-footer">
              <span className="save-state">
                {dirty ? "Unsaved vendor changes" : "No unsaved changes"}
              </span>
              <div className="save-actions">
                <button
                  className="button secondary"
                  type="button"
                  disabled={!dirty || busy}
                  onClick={() => {
                    setEnabled(vendor.enabled);
                    setTimezone(vendor.timezone);
                    setToken("");
                    setError("");
                  }}
                >
                  Discard
                </button>
                <button
                  className="button primary"
                  type="submit"
                  disabled={!dirty || busy}
                >
                  {busy ? "Saving…" : "Save changes"}
                </button>
              </div>
            </div>
          </form>
        </>
      )}
    </div>
  );
}
