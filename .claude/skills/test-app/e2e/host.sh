# Sourced by every live script: picks the session host as r-loop does — HERDR_PANE_ID set → herdr, else
# HC_TERMINAL_ID set → herdr-app (HOST=app), else none — and wraps every host call the scripts make.
# On the app an agent is the hc terminal labelled rloop.agent=<name>; every hc call runs without HC_TERMINAL_ID, and
# HERDR_APP_HOME and HC_SOCKET are pinned so r-loop under tui-session's scratch XDG still reaches the window and core.
if [ -n "${HERDR_PANE_ID:-}" ]; then HOST=herdr HOST_SELF=$HERDR_PANE_ID
elif [ -n "${HC_TERMINAL_ID:-}" ]; then HOST=app HOST_SELF=$HC_TERMINAL_ID
else HOST= HOST_SELF=
fi
if [ "$HOST" = app ]; then
  export HERDR_APP_HOME="${HERDR_APP_HOME:-${XDG_CONFIG_HOME:-$HOME/.config}/herdr-app}"
  export HC_SOCKET="${HC_SOCKET:-${XDG_STATE_HOME:-$HOME/.local/state}/herdr-core/${HC_SESSION:-default}/default.sock}"
fi

hc_() { env -u HC_TERMINAL_ID hc "$@"; }

host_up() {
  case "$HOST" in
    herdr) herdr status server > /dev/null 2>&1 ;;
    app) hc_ server status --json > /dev/null 2>&1 && herdr-app status > /dev/null 2>&1 ;;
    *) return 1 ;;
  esac
}

host_needs_herdr() {
  [ "$HOST" = herdr ] || { echo "NOT RUN: $1 needs herdr"; exit 3; }
}

host_env() {
  case "$HOST" in
    herdr) echo "-u HC_TERMINAL_ID HERDR_PANE_ID=$1" ;;
    app) echo "-u HERDR_PANE_ID HC_TERMINAL_ID=$1" ;;
  esac
}

ws_open() {
  _h_cwd=${2:-${TMPDIR:-/tmp}}
  case "$HOST" in
    herdr)
      _h_j=$(herdr workspace create --cwd "$_h_cwd" --label "$1" --no-focus) || return 1
      printf '%s' "$_h_j" | jq -r '"\(.result.workspace.workspace_id) \(.result.root_pane.pane_id)"' ;;
    app)
      _h_f=$(herdr-app status) || return 1
      _h_j=$(herdr-app do space.new --name "$1" --folder "$_h_cwd") || return 1
      herdr-app do space.select --id "$(printf '%s' "$_h_f" | jq -r .space)" > /dev/null 2>&1
      herdr-app do tab.select --id "$(printf '%s' "$_h_f" | jq -r .tab)" > /dev/null 2>&1
      herdr-app do pane.focus --pane "$(printf '%s' "$_h_f" | jq -r .focused_pane)" > /dev/null 2>&1
      printf '%s' "$_h_j" | jq -r '"\(.space) \(.terminal)"' ;;
    *) return 1 ;;
  esac
}

ws_close() {
  case "$HOST" in
    herdr) herdr workspace close "$1" ;;
    app) herdr-app do space.close --id "$1" --confirmed true ;;
  esac
}

ws_alive() {
  case "$HOST" in
    herdr) herdr workspace list | jq -e --arg id "$1" '.result.workspaces[] | select(.workspace_id==$id)' > /dev/null ;;
    app) herdr-app ls | jq -e --arg id "$1" '.spaces[] | select(.id==$id)' > /dev/null ;;
    *) return 1 ;;
  esac
}

hc_terminal() {
  _h_t=$(hc_ ls --json --label "rloop.agent=$1" 2>/dev/null | jq -r '.terminals[0].address // empty')
  [ -n "$_h_t" ] || _h_t=$(herdr-app ls 2>/dev/null | jq -r --arg p "$1" '[.spaces[].tabs[].terminals[] | select(.pane==$p) | .terminal][0] // empty')
  printf '%s\n' "${_h_t:-$1}"
}

screen() {
  case "$HOST" in
    herdr)
      case "${2:-}" in
        "") herdr agent read "$1" --source visible ;;
        recent) herdr agent read "$1" ;;
        *) herdr agent read "$1" --source recent-unwrapped --lines "$2" ;;
      esac ;;
    app)
      case "${2:-}" in
        "") set -- "$1" ;;
        recent) set -- "$1" --lines 100 ;;
        *) set -- "$1" --lines "$2" ;;
      esac
      _h_t=$(hc_terminal "$1"); shift
      hc_ read --json "$@" "$_h_t" | jq -r .text ;;
  esac
}

pane_screen() {
  case "$HOST" in
    herdr) herdr pane read "$1" ;;
    app) screen "$1" recent ;;
  esac
}

keys() {
  case "$HOST" in
    herdr) case "$1" in *:*) herdr pane send-keys "$@" ;; *) herdr agent send-keys "$@" ;; esac ;;
    app) _h_t=$(hc_terminal "$1"); shift; hc_ keys --json "$_h_t" "$@" ;;
  esac
}

prompt() {
  case "$HOST" in
    herdr) herdr agent prompt "$1" "$2" ;;
    app) hc_ send --json "$(hc_terminal "$1")" -- "$2" ;;
  esac
}

agent_state() {
  case "$HOST" in
    herdr) _h_s=$(herdr agent get "$1" 2>/dev/null | jq -r '.result.agent.agent_status // empty' 2>/dev/null) ;;
    app)
      _h_t=$(hc_ ls --json --label "rloop.agent=$1" 2>/dev/null | jq -r '.terminals[0].address // empty')
      _h_s=
      [ -z "$_h_t" ] || _h_s=$(hc_ get --json "$_h_t" 2>/dev/null | jq -r 'if .state == "finished" then "gone" else (.status.state // "unknown") end' 2>/dev/null) ;;
  esac
  printf '%s\n' "${_h_s:-gone}"
}

agents_json() {
  case "$HOST" in
    herdr) herdr agent list ;;
    app)
      for _h_t in $(hc_ ls --json | jq -r '.terminals[].address'); do hc_ get --json "$_h_t" 2>/dev/null; done \
        | jq -s '{result: {agents: [.[] | {pane_id: .address, agent: ((.status.app // "") | sub("-code$"; "")),
            agent_status: (.status.state // "unknown"), cwd: (.foreground.cwd // .cwd // ""), foreground_cwd: (.foreground.cwd // ""),
            terminal_title_stripped: (.title // ""), name: (.labels["rloop.agent"] // "")}]}}' ;;
  esac
}

agent_start() {
  _h_n=$1 _h_p=$2 _h_k=$3; shift 3
  case "$HOST" in
    herdr) herdr agent start "$_h_n" --kind "$_h_k" --pane "$_h_p" -- "$@" ;;
    app) hc_ label --json "$_h_p" "rloop.agent=$_h_n" && hc_ send --json --force "$_h_p" "exec $*" ;;
  esac
}

pane_list() { herdr pane list; }
pane_layout() { herdr pane layout --pane "$1"; }
pane_split() { herdr pane split "$1" --direction right --no-focus --cwd "${2:-/tmp}"; }

shot() {
  case "$HOST" in
    app) herdr-app shot "$1" ;;
    *) echo "no shot on herdr" ;;
  esac
}
