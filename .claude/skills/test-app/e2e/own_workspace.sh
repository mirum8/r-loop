# Sourced by a live script right after its host guard: re-runs that script with HERDR_PANE_ID (herdr) or HC_TERMINAL_ID
# (herdr-app) on a fresh workspace of its own, so its watchdog and steps never open beside the maintainer's pane, then closes it.
if [ -z "${R_LOOP_TEST_WS:-}" ]; then
  ws=$(ws_open "r-loop test $(basename "$0" .sh)") || { echo "FAIL workspace open"; exit 1; }
  R_LOOP_TEST_WS=${ws% *}
  ws_pane=${ws#* }
  trap 'ws_close "$R_LOOP_TEST_WS" > /dev/null 2>&1' EXIT INT TERM
  case "$(head -1 "$0")" in *bash*) ws_sh=bash ;; *) ws_sh=sh ;; esac
  env $(host_env "$ws_pane") R_LOOP_TEST_WS="$R_LOOP_TEST_WS" "$ws_sh" "$0" "$@"
  exit $?
fi
