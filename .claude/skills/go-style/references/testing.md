# Testing style

Plain `testing`. No testify, no gomock, no assertion DSL.

## Name the test after the behaviour
`TestWritesOnProdAreRefusedBeforeAnyRequest` (`internal/apex/client_test.go`),
`TestErrorsAreNeverStored` (`internal/memo/memo_test.go`) — the name is the specification sentence. Not
`TestRank2`. Real incidents become named tests with the id in them.

## Failure messages: call, got, want, why
`t.Fatalf` when continuing would cascade; `t.Errorf` otherwise. Include the parenthetical *why* when the
expectation is non-obvious.

## Fakes, not mocks; httptest for HTTP
Hand-written fakes (`fakeClient`, `internal/copilot/fake_test.go`). For HTTP use `httptest.NewServer` against the real
client (`internal/apex/support_test.go`, `internal/uipath/helpers_test.go`); UiPath's validator requires a
`cloud.uipath.com` URL, so its tests keep that URL and redirect the client's transport. Package-level test
hooks are unexported vars (`submitSettle`, `processRetryDelay`) — other packages' tests use a fake `Client`.
Reset process-level state with the `Reset…ForTest` hooks (`config.ResetForTest`, `copilot.ResetSessionCacheForTest`).

## Contexts, dirs, helpers
`t.Context()`, `t.Chdir()`, `t.TempDir()`. Any helper that calls `t.Errorf`/`t.Fatalf` starts with `t.Helper()`.
Subtests only when the axis is data.

## Parity tests are the gate
Golden fixtures prove a refactor kept behaviour: `internal/queueitem/parity_test.go` replays an order through
the builder + UiPath serializer against `testdata/expected_v1_33_1.json`; `internal/copilot/testdata/ts-sessions.json`
(a TS-written session file) proves `Connect` reuses the TS on-disk cache. If a change alters their output, that is
a product decision to surface — not a fixture to update.

## Concurrency tests
Count upstream calls (a second read of a settled job makes 0 log/listing calls), assert peak in-flight ≤ the bound
(`internal/copilot/settings/read_test.go`), prove writes after a cached read still re-read live. Everything
runs under `-race` (`make test`).

## Running
```
make check                                           # the gate
go test -race ./internal/apex/ -run TestErrorsAre -v # one package, one test
```
`errcheck` is off for `_test.go` only (`.golangci.yml`); don't extend that to production code.
