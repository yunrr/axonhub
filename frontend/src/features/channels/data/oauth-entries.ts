import { NamedOAuthCredentials, OAUTH_CREDENTIAL_REF } from './schema';

// Channel types whose credentials are always OAuth subscriptions.
export const ALWAYS_OAUTH_CHANNEL_TYPES = ['antigravity', 'github_copilot', 'xai_subscription'];

export function newOAuthEntryID() {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return `oauth-${crypto.randomUUID()}`;
  }
  return `oauth-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

/** Normalize an expires_at value (epoch seconds/millis or ISO string) into an RFC3339 string. */
export function normalizeExpiresAt(value: unknown): string | undefined {
  if (value === null || value === undefined || value === '') return undefined;
  if (typeof value === 'number' || /^\d{10,13}$/.test(String(value))) {
    const num = Number(value);
    const ms = num < 1e12 ? num * 1000 : num;
    const date = new Date(ms);
    return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
  }
  if (typeof value === 'string') {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
  }
  return undefined;
}

/**
 * Parse one imported subscription: an OAuth credentials JSON (auth.json style)
 * or the Antigravity "refreshToken|projectID" composite.
 */
export function parseOAuthCredentialText(
  text: string
): { entry: NamedOAuthCredentials } | { error: 'invalid_json' | 'missing_token' | 'invalid_format' } {
  const trimmed = text.trim();
  if (trimmed.startsWith('{')) {
    let obj: Record<string, unknown>;
    try {
      obj = JSON.parse(trimmed);
    } catch {
      return { error: 'invalid_json' };
    }
    const accessToken = (obj.access_token ?? obj.accessToken) as string | undefined;
    const refreshToken = (obj.refresh_token ?? obj.refreshToken) as string | undefined;
    if (!accessToken && !refreshToken) {
      return { error: 'missing_token' };
    }
    const name = ((obj.email ?? obj.account_id ?? obj.accountID ?? obj.project_id ?? obj.projectID) as string | undefined) || undefined;
    return {
      entry: {
        id: newOAuthEntryID(),
        name,
        projectId: ((obj.project_id ?? obj.projectID) as string | undefined) || undefined,
        credentials: {
          accessToken: accessToken || undefined,
          refreshToken: refreshToken || undefined,
          clientID: ((obj.client_id ?? obj.clientID) as string | undefined) || undefined,
          expiresAt: normalizeExpiresAt(obj.expires_at ?? obj.expiry ?? obj.expiresAt),
          tokenType: ((obj.token_type ?? obj.tokenType) as string | undefined) || undefined,
          scopes: Array.isArray(obj.scopes) ? (obj.scopes as string[]) : undefined,
        },
      },
    };
  }

  const sep = trimmed.indexOf('|');
  if (sep > 0 && !trimmed.includes(' ')) {
    const refreshToken = trimmed.slice(0, sep);
    const projectId = trimmed.slice(sep + 1);
    if (refreshToken && projectId) {
      return { entry: { id: newOAuthEntryID(), name: projectId, projectId, credentials: { refreshToken } } };
    }
  }

  return { error: 'invalid_format' };
}

/** Stable fingerprint of a credential entry for de-duplication. */
export function credentialTokenOf(entry: NamedOAuthCredentials): string {
  return `${entry.credentials?.refreshToken ?? ''}|${entry.credentials?.accessToken ?? ''}|${entry.projectId ?? ''}`;
}

/**
 * Wrap the legacy single-OAuth credential (stored in the apiKey field) into a
 * named entry carrying the backend sentinel ref, so existing disable records
 * keep matching after the channel migrates to the oauths layout.
 */
export function buildLegacyOAuthEntry(apiKey: string | null | undefined): NamedOAuthCredentials | undefined {
  const trimmed = apiKey?.trim();
  if (!trimmed) {
    return undefined;
  }
  const parsed = parseOAuthCredentialText(trimmed);
  if ('error' in parsed) {
    return undefined;
  }
  return { ...parsed.entry, id: OAUTH_CREDENTIAL_REF };
}
