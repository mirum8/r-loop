#!/usr/bin/env bash
# usage: wd_call.sh <url> <tool> '<json-args>'
set -euo pipefail
url=$1 tool=$2 args=${3:-'{}'}
hdr=$(mktemp); trap 'rm -f "$hdr"' EXIT
H=(-H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream')
curl -sS -D "$hdr" -o /dev/null "${H[@]}" -X POST "$url" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"wd_call","version":"1"}}}'
sid=$(awk 'tolower($1)=="mcp-session-id:"{print $2}' "$hdr" | tr -d '\r')
S=()
[ -n "$sid" ] && S=(-H "Mcp-Session-Id: $sid")
curl -sS -o /dev/null "${H[@]}" "${S[@]}" -X POST "$url" -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'
body=$(jq -cn --arg n "$tool" --argjson a "$args" '{jsonrpc:"2.0",id:2,method:"tools/call",params:{name:$n,arguments:$a}}')
resp=$(curl -sS "${H[@]}" "${S[@]}" -X POST "$url" -d "$body")
json=$(printf '%s\n' "$resp" | sed -n 's/^data: //p' | tail -1)
[ -z "$json" ] && json=$resp
printf '%s\n' "$json" | jq -c '.result.structuredContent // .result // .error'
[ -n "$sid" ] && curl -sS -o /dev/null -X DELETE -H "Mcp-Session-Id: $sid" "$url" || true
