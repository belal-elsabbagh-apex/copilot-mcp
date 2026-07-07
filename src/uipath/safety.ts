// The single test-safety enforcement point for queue-item SpecificContent. Every
// path that hands a payload to (or toward) a robot goes through here:
//   - "force" (env-agnostic): pull_queue_item / build_queue_item — force IsApproved=false
//     so a test run can never submit a real auth.
//   - "preProdPost" (add_queue_item): force, PLUS hard asserts that the item cannot
//     point a dev-clone robot at prod — serverURL/queueUrl/NoteBucketPath must match
//     the configured pre-prod values, and no `<TO-FILL>` placeholders may remain.
//
// Pure and config-free: the caller injects the configured limits.

export interface QueueSafetyLimits {
  preProdServerUrl: string; // config uipath.serverUrlByEnv?.pre_prod ?? ""
  queueUrl: string; // config uipath.queueUrl ?? ""
  noteBucket: string; // config uipath.noteBucket ?? ""
}

export type SafetyMode = "force" | "preProdPost";

export interface GuardedContent {
  specificContent: Record<string, unknown>; // copy of the input with IsApproved forced false
  forced: string[]; // fields the guard overrode, e.g. ["IsApproved"]
}

// `<TO-FILL>`-style placeholders left over from a fixture that was never minted.
const PLACEHOLDER_RE = /<\s*TO[-_ ]?FILL/i;

// Validate + force test-safety on a SpecificContent record. Never mutates the
// input. In "preProdPost" mode, throws one Error aggregating ALL violations.
// `limits` may be omitted in "force" mode (it is only read by "preProdPost",
// where the empty default fails closed on any non-empty pointer field).
export function guardQueueItemSafety(
  sc: Record<string, unknown>,
  mode: SafetyMode,
  limits: QueueSafetyLimits = { preProdServerUrl: "", queueUrl: "", noteBucket: "" },
): GuardedContent {
  const forced: string[] = [];
  if (sc["IsApproved"] !== false) forced.push("IsApproved");
  const specificContent: Record<string, unknown> = { ...sc, IsApproved: false };
  if (mode === "force") return { specificContent, forced };

  const violations: string[] = [];
  const str = (k: string): string => {
    const v = sc[k];
    return typeof v === "string" ? v : "";
  };

  const serverURL = str("serverURL");
  if (serverURL) {
    if (!limits.preProdServerUrl) {
      violations.push(
        "item carries a serverURL but uipath.serverUrlByEnv.pre_prod is not configured — " +
          "set it so the guard can verify the callback host is pre-prod",
      );
    } else if (serverURL !== limits.preProdServerUrl) {
      violations.push(
        `serverURL '${serverURL}' is not the configured pre-prod host '${limits.preProdServerUrl}'`,
      );
    }
  }

  const queueUrl = str("queueUrl");
  if (queueUrl && queueUrl !== limits.queueUrl) {
    violations.push(
      limits.queueUrl
        ? `queueUrl '${queueUrl}' is neither empty nor the configured uipath.queueUrl`
        : `queueUrl '${queueUrl}' must be empty (uipath.queueUrl is not configured)`,
    );
  }

  const notePath = str("NoteBucketPath");
  if (notePath) {
    if (!limits.noteBucket) {
      violations.push(
        `NoteBucketPath '${notePath}' must be empty (uipath.noteBucket is not configured)`,
      );
    } else if (!notePath.startsWith(`s3://${limits.noteBucket}/`)) {
      violations.push(
        `NoteBucketPath '${notePath}' is outside the configured bucket 's3://${limits.noteBucket}/'`,
      );
    }
  }

  const placeholderKeys = Object.keys(sc).filter((k) => {
    const v = sc[k];
    return typeof v === "string" && PLACEHOLDER_RE.test(v);
  });
  if (placeholderKeys.length) {
    violations.push(
      `placeholder (<TO-FILL>) values in: ${placeholderKeys.join(", ")} — mint/fill them first`,
    );
  }

  if (violations.length) {
    throw new Error(`refusing to post queue item — ${violations.join("; ")}`);
  }
  return { specificContent, forced };
}
