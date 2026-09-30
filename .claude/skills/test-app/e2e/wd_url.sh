#!/usr/bin/env bash
# usage: wd_url.sh <sandbox>  -> the watchdog MCP URL of the live run in that repo
# maps runs/current's driver pid -> its 127.0.0.1 listen port -> the watchdog.mcp.json with that port
set -euo pipefail
sb=${1:?sandbox}
read -r _ pid < "$sb/.r-loop/runs/current"
for port in $(lsof -nP -a -p "$pid" -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR>1{split($9,a,":"); print a[length(a)]}'); do
  for f in $(ls -t "${TMPDIR:-/tmp}"/r-loop-watchdog-*/watchdog.mcp.json 2>/dev/null); do
    u=$(jq -r '.mcpServers["r-loop"].url' "$f")
    case "$u" in "http://127.0.0.1:$port/mcp/watchdog/"*) echo "$u"; exit 0 ;; esac
  done
done
echo "no watchdog url for pid $pid" >&2; exit 1
