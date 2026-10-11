// Base marker for "known, user-actionable failure — not a bug". Throwing a subclass
// of this (or being one, like NotImplementedError) classifies as `expected` by identity
// (mcp/feedback.ts classify), independent of message wording. A leaf module so config.ts
// can throw subclasses without importing feedback.ts (which imports config.ts).
export class ExpectedError extends Error {}
