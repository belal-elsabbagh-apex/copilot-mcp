// Semantic analysis of a job's result. Each rule inspects the parsed
// OutputArguments and the job's robot logs and emits severity-tagged comments.
// Ported from copilot-doctor src/outputAnalysis.ts.

import type { JobLog } from "./uipath.js";

export type CommentSeverity = "error" | "warning" | "info";

export interface OutputComment {
  severity: CommentSeverity;
  rule: string;
  message: string;
}

const ERROR_LEVELS = new Set(["error", "fatal", "critical"]);
const WARN_LEVELS = new Set(["warn", "warning"]);
const levelOf = (log: JobLog): string => (log.Level || "").toLowerCase();
const plural = (n: number, noun: string): string => `${n} ${noun}${n === 1 ? "" : "s"}`;

// Words/phrases that suggest a failure even when the log's own level is benign.
const FAILURE_TERMS = [
  "exceptions?",
  "errors?",
  "errored",
  "faults?",
  "faulted",
  "fail(?:s|ed|ing|ure)?",
  "crash(?:es|ed|ing)?",
  "abort(?:s|ed|ing)?",
  "terminat(?:e|ed|es|ing|ion)",
  "kill(?:s|ed)?",
  "halt(?:s|ed|ing)?",
  "panic(?:ked|king)?",
  "fatal",
  "critical",
  "severe",
  "unable to",
  "not able",
  "cannot",
  "can[’'`]?t",
  "could ?n[o’'`]?t",
  "did ?n[o’'`]?t",
  "was ?n[o’'`]?t able",
  "no response",
  "not found",
  "missing",
  "invalid",
  "unexpected",
  "unhandled",
  "illegal",
  "denied",
  "rejected",
  "refused",
  "unauthori[sz]ed",
  "forbidden",
  "expired",
  "access denied",
  "permission denied",
  "invalid credentials",
  "time(?:d)? ?out",
  "timeout",
  "unreachable",
  "disconnected",
  "connection (?:refused|reset|lost|closed|error)",
  "reset by peer",
  "retries exhausted",
  "max(?:imum)? retries",
  "gave up",
  "giving up",
  "stack ?trace",
  "traceback",
  "null ?reference",
  "null ?pointer",
  "out of memory",
  "stack overflow",
  "overflow",
  "deadlock",
  "segfault",
  "segmentation fault",
  "corrupt(?:s|ed|ion|ing)?",
  "broken",
  "bad request",
  "internal server error",
  "service unavailable",
  "bad gateway",
  "gateway timeout",
  "too many requests",
];
const FAILURE_PATTERN = new RegExp(`\\b(?:${FAILURE_TERMS.join("|")})\\b`, "i");

interface AnalysisContext {
  output: Record<string, unknown>;
  logs: JobLog[];
}
interface AnalysisRule {
  id: string;
  run(ctx: AnalysisContext): OutputComment[];
}

const resultFailureRule: AnalysisRule = {
  id: "result-failure",
  run: ({ output }) => {
    const result = output["out_Result"];
    return typeof result === "string" && result.toLowerCase() === "failure"
      ? [
          {
            severity: "error",
            rule: "result-failure",
            message: 'Automation reported out_Result = "Failure".',
          },
        ]
      : [];
  },
};
const logErrorsRule: AnalysisRule = {
  id: "log-errors",
  run: ({ logs }) => {
    const n = logs.filter((l) => ERROR_LEVELS.has(levelOf(l))).length;
    return n === 0
      ? []
      : [
          {
            severity: "error",
            rule: "log-errors",
            message: `${plural(n, "error log")} during execution.`,
          },
        ];
  },
};
const logWarningsRule: AnalysisRule = {
  id: "log-warnings",
  run: ({ logs }) => {
    const n = logs.filter((l) => WARN_LEVELS.has(levelOf(l))).length;
    return n === 0
      ? []
      : [
          {
            severity: "warning",
            rule: "log-warnings",
            message: `${plural(n, "warning log")} during execution.`,
          },
        ];
  },
};
const logFailureIndicatorsRule: AnalysisRule = {
  id: "log-failure-indicators",
  run: ({ logs }) => {
    const n = logs.filter((l) => {
      const level = levelOf(l);
      if (ERROR_LEVELS.has(level) || WARN_LEVELS.has(level)) return false;
      return FAILURE_PATTERN.test(l.Message || "");
    }).length;
    return n === 0
      ? []
      : [
          {
            severity: "warning",
            rule: "log-failure-indicators",
            message: `${plural(n, "log message")} mention errors or failures.`,
          },
        ];
  },
};

const RULES: AnalysisRule[] = [
  resultFailureRule,
  logErrorsRule,
  logWarningsRule,
  logFailureIndicatorsRule,
];

export function analyzeOutput(
  output: Record<string, unknown>,
  logs: JobLog[] = [],
): OutputComment[] {
  return RULES.flatMap((rule) => rule.run({ output, logs }));
}

export const isFailureLog = (log: JobLog): boolean =>
  ERROR_LEVELS.has(levelOf(log)) || FAILURE_PATTERN.test(log.Message || "");
