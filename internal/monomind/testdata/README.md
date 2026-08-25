# Test fixtures

`fixtures/*.ndjson` are verbatim copies of monomind's golden Agent Exec
Protocol transcripts (monomind repo: `doc/agent-exec-protocol/fixtures/`,
protocol v1 rev 4). They are the caller-side contract — mono-agent's client
tests validate its event model against them. Regenerate by copying after any
monomind protocol revision bump.

`fake-monomind.sh` is a scripted monomind stand-in for subprocess tests: it
speaks the handshake, scan, and a tool-bridged exec turn; `fake-monolith.sh`
spawns a child and ignores cancellation, for the process-group kill test.
