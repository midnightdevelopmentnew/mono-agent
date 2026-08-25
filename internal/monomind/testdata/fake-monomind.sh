#!/bin/sh
# Fake monomind binary for internal/monomind subprocess tests.
# Speaks just enough of the Agent Exec Protocol v1:
#   --version --json          → handshake payload
#   agent scan --json         → sample scan
#   agent exec ...            → one tool-bridged turn: streams a tool_call,
#                               reads the matching tool_result from stdin,
#                               then finishes with success events.
#   env FAKE_MODE=hang        → exec ignores everything forever (kill test
#                               is handled by fake-monolith.sh instead).
DIR="$(cd "$(dirname "$0")" && pwd)"

if [ "$1" = "--version" ] && [ "$2" = "--json" ]; then
  echo '{"v":1,"version":"2.10.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'
  exit 0
fi

if [ "$1" = "agent" ] && [ "$2" = "scan" ]; then
  echo '{"v":1,"agents":[{"id":"claude","installed":true,"binary":"/usr/local/bin/claude","version":"2.1.220","install_hint":""},{"id":"codex","installed":false,"binary":null,"version":null,"install_hint":"npm install -g @openai/codex"}]}'
  exit 0
fi

if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then
  # Drain flags; the prompt value is irrelevant to the script.
  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/app","pid":4212}'
  echo '{"v":1,"type":"session","session_id":"th_fake"}'
  echo '{"v":1,"type":"assistant","text":"Creating nodes…"}'
  echo '{"v":1,"type":"tool_call","id":"tc_1","name":"create_nodes","args":{"count":2}}'
  # Read one tool_result frame from stdin (the client's reply).
  read -r line
  echo "$line" >&2
  echo '{"v":1,"type":"tool_result","id":"tc_1","ok":true,"result":{"text":"created 2 nodes"}}'
  echo '{"v":1,"type":"assistant","text":"Created the nodes."}'
  echo '{"v":1,"type":"usage","input_tokens":400,"output_tokens":40,"cost_usd":0.002}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"Created the nodes.","input_tokens":400,"output_tokens":40,"cost_usd":0.002}'
  echo '{"v":1,"type":"done","exit_code":0}'
  exit 0
fi

if [ "$1" = "org" ] && [ "$2" = "list" ]; then
  echo '{"v":1,"items":[{"name":"growth","roles":3,"schedule":"manual","status":"running","goal":"grow the thing"}]}'
  exit 0
fi

if [ "$1" = "org" ] && [ "$2" = "status" ] && [ "$3" = "growth" ]; then
  echo '{"v":1,"name":"growth","status":"running","run":"r1","pid":9999,"paused":false}'
  exit 0
fi

if [ "$1" = "org" ] && [ "$2" = "status" ] && [ "$3" = "missing-org" ]; then
  echo "org 'missing-org' not found" >&2
  exit 1
fi

if [ "$1" = "org" ] && [ "$2" = "events" ] && [ "$3" = "growth" ]; then
  echo '{"id":"e1","ts":1,"org":"growth","run":"r1","type":"status","msg":"started"}'
  echo '{"id":"e2","ts":2,"org":"growth","run":"r1","type":"message","from":"lead","to":"coder","subject":"start"}'
  exit 0
fi

echo "fake-monomind: unsupported invocation: $*" >&2
exit 2
