// UiPath Orchestrator auth token resolution: OAuth client-credentials (optional) with
// a static bearer/PAT fallback. `resolveBearerToken` is the single function
// `uipathRequest` (uipath.ts) calls to get the value that goes into the
// `Authorization: Bearer` header — everything else in this file is in service of that.

import { onConfigReload, type UipathConfig } from "../config/config.js";
import { prop } from "../shared/util.js";

type OAuthConfig = NonNullable<UipathConfig["oauth"]>;

interface CachedToken {
  accessToken: string;
  expiresAt: number; // epoch ms
}

// Refresh this long before the token's real expiry so a request never races a
// just-expired token.
const EXPIRY_SKEW_MS = 60_000;

let cached: CachedToken | undefined;

// Any config edit invalidates the cache — cheap, and avoids a rotated client secret
// (same clientId) surviving under a stale token.
onConfigReload(() => {
  cached = undefined;
});

// Derive the identity-server token endpoint from the org segment of orchestratorUrl
// (https://cloud.uipath.com/{org}/{tenant}/orchestrator_ -> .../{org}/identity_/connect/token).
// Only used when oauth.tokenUrl isn't set explicitly.
function defaultTokenUrl(orchestratorUrl: string): string {
  const m = orchestratorUrl.match(/^(https:\/\/cloud\.uipath\.com\/[^/]+)\//);
  if (!m) {
    throw new Error(
      "cannot derive uipath.oauth.tokenUrl from orchestratorUrl; set uipath.oauth.tokenUrl explicitly",
    );
  }
  return `${m[1]}/identity_/connect/token`;
}

async function fetchOAuthToken(oauth: OAuthConfig, orchestratorUrl: string): Promise<CachedToken> {
  const tokenUrl = oauth.tokenUrl ?? defaultTokenUrl(orchestratorUrl);
  const body = new URLSearchParams({
    grant_type: "client_credentials",
    client_id: oauth.clientId,
    client_secret: oauth.clientSecret,
    ...(oauth.scope ? { scope: oauth.scope } : {}),
  });
  const res = await fetch(tokenUrl, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: body.toString(),
  });
  const text = await res.text();
  if (res.status >= 400) {
    throw new Error(`UiPath OAuth token request -> ${res.status}: ${text.slice(0, 300)}`);
  }
  let json: unknown;
  try {
    json = JSON.parse(text);
  } catch {
    throw new Error("UiPath OAuth token response was not valid JSON");
  }
  const accessToken = prop(json, "access_token");
  if (typeof accessToken !== "string" || !accessToken) {
    throw new Error("UiPath OAuth token response missing access_token");
  }
  const expiresIn = prop(json, "expires_in");
  const expiresInSeconds = typeof expiresIn === "number" ? expiresIn : 3600;
  return { accessToken, expiresAt: Date.now() + expiresInSeconds * 1000 - EXPIRY_SKEW_MS };
}

// Resolve the token to send as `Authorization: Bearer <token>`. Tries OAuth first
// when configured (cached across calls until it's near expiry); falls back to the
// static bearer/PAT if OAuth isn't configured, or if its token request fails.
export async function resolveBearerToken(cfg: UipathConfig): Promise<string> {
  if (cfg.oauth) {
    if (cached && Date.now() < cached.expiresAt) return cached.accessToken;
    try {
      cached = await fetchOAuthToken(cfg.oauth, cfg.orchestratorUrl);
      return cached.accessToken;
    } catch (e) {
      if (cfg.bearer) return cfg.bearer;
      throw e;
    }
  }
  if (!cfg.bearer) {
    throw new Error("uipath config has neither oauth nor bearer configured");
  }
  return cfg.bearer;
}

// Test-only: clear the cached OAuth token between cases.
export function resetOAuthTokenCache(): void {
  cached = undefined;
}
