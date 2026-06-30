# OPTUM Auth Submit — `submit-button-unactionable` (Same-As-Requesting + Submit disabled)

**Status:** Open — under investigation (root cause identified, fix not yet defined)
**Portal:** OPTUM Care (One Healthcare ID SSO → Curo Provider Module, `curoai.optum.com/promod`)
**Bot package:** `apex.auth.submit@1.0.816` (`@rpa/portal-optum v1.0.0`)
**UiPath folder:** `Authorization` (prod)
**Account route:** `OPTUM OSSM Credentials`

## Summary

The OPTUM auth-submit bot completes login, member search, and all data-entry
sections, then **fails to submit** because two portal buttons remain `disabled`:

1. **Servicing Provider → "Same As Requesting Provider"** button is `disabled`, so
   the servicing provider is never copied/set.
2. **Submit** button (`[data-cy="intakeSubmitButton"]`) is `disabled`, so the
   request is never submitted.

In both cases Playwright resolves the element but sits in
`waiting for element to be visible, enabled and stable → element is not enabled`
for the full 60s click timeout, then gives up. The run ends with:

- `"result": "FAILURE"`
- `"reason": "SessionExpired: submit-button-unactionable"`
- post-submit classification `approval=PENDING` (nothing was actually submitted)
- final `ABORT: Submit unconfirmed`

The `SessionExpired` label appears to be a **misnomer** — the session is alive
(cookies present, page interactive, screenshots rendering). The blocker is that the
intake form never reaches a valid/complete state, so the portal keeps Submit disabled.

At the end of both runs the portal still flags these sections as required/incomplete:

- **Servicing Provider** — empty (the Same-As click never landed)
- **Procedure modifiers** — every CPT/HCPCS row shows `Select a modifier... *Required`
  (codes 99214, 20610, J3301, J2003)
- **Notes** — `NotesRequired`, empty

The portal also raised **duplicate warnings** on CPTs 99214, 20610, J3301, J2003
("Code 99214 also appears in these active auth(s): 19691745, 19694262, 17245591,
17197143, 19208624"). Not necessarily blocking on their own, but the page asks for a
duplicate acknowledgement before submitting.

## Affected order

| Field | Value |
|---|---|
| Order UID | `a1f504e2-4226-4b39-b417-a2e4f1ed713e` |
| Member | GUERRERO, RONALD (`913581750001`) |
| Provider | OSSM (NPI `1750021606`, Orthopedics) |
| Requesting = Servicing | yes — BE `referredFrom` and `referredTo` are identical, so the bot chose `matchKind: "same-as-requesting"` |
| Service setting / POS | Outpatient / 11 - Office |
| Procedures | 99214, 20610, J3301, J2003 |
| Diagnoses | M17.11, M17.12 |

## The two failures

Both runs are the **same order**, **same package**, **same failure fingerprint**,
~14 hours apart. This is **reproducible**, not a transient/flaky session issue.

### Run A — job `f634fe50-2fb9-4085-b88d-d976b5ceaaae`

- Started: 2026-06-29 17:43:10 UTC · Total: 252.7s
- Step 5 "Same As Requesting Provider": `disabled` → 60s timeout (17:44:44 → 17:45:44)
- Step 11 SUBMIT: `disabled` → 60s timeout (17:45:58 → 17:46:58)
- Result: FAILURE · reason `SessionExpired: submit-button-unactionable` · approval PENDING
- Member eligibility at time of run: **Blue Shield of California, Active**,
  Sub ID `913581750001`, PCP GUZMAN, BENNY J

### Run B — job `5977e15e-ef04-45dd-8024-b8582d4505ff`

- Started: 2026-06-30 07:00:42 UTC · Total: 255.9s
- Step 5 "Same As Requesting Provider": `disabled` → 60s timeout (07:02:21 → 07:03:21)
- Step 11 SUBMIT: `disabled` → 60s timeout (07:03:36 → 07:04:36)
- Result: FAILURE · reason `SessionExpired: submit-button-unactionable` · approval PENDING
- Member eligibility at time of run: **Sub ID `--`, PCP `--`, Terminated 06/30/2026**

### Side-by-side

| | Run A (`f634fe50`) | Run B (`5977e15e`) |
|---|---|---|
| Date (UTC) | 2026-06-29 17:43 | 2026-06-30 07:00 |
| Total runtime | 252.7s | 255.9s |
| Result | FAILURE | FAILURE |
| Reason | `SessionExpired: submit-button-unactionable` | same |
| Same-As-Requesting button | `disabled` → 60s timeout | `disabled` → 60s timeout |
| Submit button | `disabled` → 60s timeout | `disabled` → 60s timeout |
| Post-submit approval | PENDING (nothing submitted) | PENDING (nothing submitted) |
| Still-required at end | Servicing Provider, procedure modifiers, Notes | same |
| Duplicate-flagged CPTs | 99214, 20610, J3301, J2003 | same |
| Member eligibility | Blue Shield CA, Active | Sub ID `--`, Terminated 06/30/2026 |

The notable difference is member eligibility: it was **Active** in Run A and had
**lapsed/terminated** by Run B. Despite that change, the bot failed at the **exact same
step** both times — which rules out eligibility as the cause and points squarely at the
disabled-button / incomplete-form logic.

## Observed element state

Both the Same-As-Requesting and Submit buttons resolve to a `disabled` element, e.g.:

```html
<button disabled data-cy="same-as-requesting-button" ...>Same As Requesting Provider</button>
<button disabled data-cy="intakeSubmitButton" ...>Submit</button>
```

Playwright click log (identical shape for both buttons):

```
- locator resolved to <button disabled ...>
- attempting click action
  - waiting for element to be visible, enabled and stable
    - element is not enabled
  - retrying click action
  ... (repeats until 60000ms timeout exceeded)
```

## Root cause (current understanding)

The **disabled "Same As Requesting Provider" button is the first domino.** Because that
click never lands, the Servicing Provider section stays empty/required; combined with the
unfilled procedure modifiers and empty Notes, the intake form never validates, so the
portal keeps the Submit button disabled for the entire run. The bot's `same-as-requesting`
decision (servicing == requesting) is itself correct per the BE data — the failure is in
the portal form reaching a submittable state, not in the data or the session.

Exactly **why** the Same-As button is disabled in the first place is not yet confirmed from
the logs alone (candidate explanations: requesting-provider selection not fully committed in
the portal's eyes, an unmet required sub-field, etc.). The job videos / `post-submit.html`
snapshots would help confirm.

> Fix intentionally not documented yet — see follow-up.

## Evidence / artifacts

- Robot logs: jobs `f634fe50-...` and `5977e15e-...` in UiPath folder `Authorization` (prod)
- Per-run screenshots (`section-rendering-provider.png`, `submit-unconfirmed.png`,
  `post-submit.png`) and `post-submit.html` snapshots captured by the bot
- Run B video recording available via the job's `videoUrl`
