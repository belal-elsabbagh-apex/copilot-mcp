export const meta = {
  name: 'invariant-compliance-sweep',
  description: 'Check pending changes (or the full src tree) against every hard invariant in .claude/rules/, with adversarial verification of each finding',
  whenToUse: 'Before a release, after a feature branch touches a write path (order minting, settings sync, UiPath actions), or any time you want a second opinion on whether a change broke a documented safety invariant.',
  phases: [
    { title: 'Scan', detail: 'one finder agent per invariant rule in .claude/rules/' },
    { title: 'Verify', detail: 'adversarial refute votes per finding, majority must survive' },
  ],
}

const RULES = [
  { id: 'env-profile-required', file: '.claude/rules/env-profile-required.md' },
  { id: 'stdout-jsonrpc-channel', file: '.claude/rules/stdout-jsonrpc-channel.md' },
  { id: 'prod-preprod-isolation', file: '.claude/rules/prod-preprod-isolation.md' },
  { id: 'order-minting-single-engine', file: '.claude/rules/order-minting-single-engine.md' },
  { id: 'uipath-writes-guarded', file: '.claude/rules/uipath-writes-guarded.md' },
  { id: 'settings-sync-plan-apply', file: '.claude/rules/settings-sync-plan-apply.md' },
  { id: 'tool-response-contract', file: '.claude/rules/tool-response-contract.md' },
]

const FINDINGS_SCHEMA = {
  type: 'object',
  properties: {
    findings: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          file: { type: 'string' },
          line: { type: 'number' },
          summary: { type: 'string' },
          evidence: { type: 'string' },
        },
        required: ['file', 'summary', 'evidence'],
      },
    },
  },
  required: ['findings'],
}

const VERDICT_SCHEMA = {
  type: 'object',
  properties: {
    refuted: { type: 'boolean' },
    reason: { type: 'string' },
  },
  required: ['refuted', 'reason'],
}

const scope = args && args.mode === 'full'
  ? 'the entire src/ tree'
  : 'the pending diff (run `git status` and `git diff main...HEAD` — include both staged/unstaged changes and committed-but-unmerged commits — to find it)'

function finderPrompt(rule) {
  return `You are auditing the copilot-mcp repo (an MCP server) for violations of ONE specific ` +
    `hard invariant. First read ${rule.file} in full — it states the rule, why it matters, and a ` +
    `"Violation signature" section describing concrete patterns to look for. Then check ${scope} ` +
    `for violations of THIS rule only — ignore anything that violates a different rule or is just ` +
    `a style/quality issue. For each real violation, report the file, best-effort line number, a ` +
    `one-sentence summary, and the specific evidence (code snippet or quoted reasoning) proving it ` +
    `violates the rule as written. If there are no violations, return an empty findings array — do ` +
    `not invent a finding to have something to report.`
}

function refutePrompt(rule, finding) {
  return `A reviewer flagged this as a violation of the invariant in ${rule.file}: ` +
    `"${finding.summary}" in ${finding.file}${finding.line ? ':' + finding.line : ''}. ` +
    `Evidence given: ${finding.evidence}\n\n` +
    `Read ${rule.file} and the actual code at the cited location. Try to REFUTE this finding — ` +
    `is it actually compliant (e.g. the guard is called one level up, this is the documented ` +
    `exception, the evidence misreads the code)? Default to refuted=false (finding stands) if ` +
    `you are not confident it's a false positive.`
}

const results = await pipeline(
  RULES,
  rule => agent(finderPrompt(rule), { label: `scan:${rule.id}`, phase: 'Scan', schema: FINDINGS_SCHEMA }),
  async (scanResult, rule) => {
    const findings = (scanResult && scanResult.findings) || []
    if (!findings.length) return { rule, findings: [] }

    const judged = await parallel(findings.map(f => () =>
      parallel([1, 2, 3].map(() => () =>
        agent(refutePrompt(rule, f), { label: `verify:${rule.id}`, phase: 'Verify', schema: VERDICT_SCHEMA })
      )).then(votes => {
        const refutedCount = votes.filter(Boolean).filter(v => v.refuted).length
        return { ...f, ruleId: rule.id, ruleFile: rule.file, survived: refutedCount < 2, refutedCount }
      })
    ))

    return { rule, findings: judged }
  }
)

const allFindings = results.filter(Boolean).flatMap(r => r.findings)
const confirmed = allFindings.filter(f => f.survived)
const refuted = allFindings.filter(f => !f.survived)

if (refuted.length) {
  log(`${refuted.length} candidate finding(s) refuted by majority vote and dropped`)
}
log(`${confirmed.length} confirmed violation(s) across ${RULES.length} rules`)

return {
  scope: args && args.mode === 'full' ? 'full' : 'diff',
  rulesChecked: RULES.length,
  confirmed,
  refuted,
}
