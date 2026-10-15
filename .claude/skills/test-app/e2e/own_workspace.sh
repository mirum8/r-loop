# Sourced by a live script right after its herdr guards: re-runs that script with HERDR_PANE_ID on the root pane of a
# fresh herdr workspace, so its watchdog and steps never open beside the maintainer's pane, then closes it.
if [ -z "${R_LOOP_TEST_WS:-}" ]; then
  ws_json=$(herdr workspace create --cwd "${TMPDIR:-/tmp}" --label "r-loop test $(basename "$0" .sh)" --no-focus) || { echo "FAIL herdr workspace create"; exit 1; }
  R_LOOP_TEST_WS=$(printf '%s' "$ws_json" | jq -r '.result.workspace.workspace_id')
  ws_pane=$(printf '%s' "$ws_json" | jq -r '.result.root_pane.pane_id')
  trap 'herdr workspace close "$R_LOOP_TEST_WS" > /dev/null 2>&1' EXIT INT TERM
  case "$(head -1 "$0")" in *bash*) ws_sh=bash ;; *) ws_sh=sh ;; esac
  R_LOOP_TEST_WS="$R_LOOP_TEST_WS" HERDR_PANE_ID="$ws_pane" "$ws_sh" "$0" "$@"
  exit $?
fi
