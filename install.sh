#!/bin/sh
set -eu

prefix="${PREFIX:-$HOME/.local/bin}"
root="$(cd "$(dirname "$0")" && pwd)"

command -v go >/dev/null 2>&1 || { echo "install.sh: go is not on PATH" >&2; exit 1; }

version="$(git -C "$root" describe --always --dirty 2>/dev/null || echo dev)"

mkdir -p "$prefix"
cd "$root"
go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$prefix/r-loop" ./cmd/r-loop

"$prefix/r-loop" --version
echo "installed to $prefix/r-loop"
(cd / && "$prefix/r-loop" --migrate-config) || echo "note: finish ~/.config/r-loop/config.yaml by hand"

case ":$PATH:" in
  *":$prefix:"*) ;;
  *) echo "note: $prefix is not on PATH" ;;
esac

for dep in herdr git; do
  command -v "$dep" >/dev/null 2>&1 || echo "note: $dep is not on PATH; r-loop needs it at run time"
done

herdr_config="${XDG_CONFIG_HOME:-$HOME/.config}/herdr/config.toml"
herdr_block='# >>> r-loop
[ui.sidebar.spaces]
rows = [["state_icon", "workspace"],
        [{ token = "$rloop_wait", fg = "#E0A458", bold = true }, { token = "$rloop", fg = "#6E9FC4" }, "branch", "git_status"]]
# <<< r-loop'

mkdir -p "$(dirname "$herdr_config")"
touch "$herdr_config"
stripped="$(sed '/^# >>> r-loop$/,/^# <<< r-loop$/d' "$herdr_config")"
if printf '%s\n' "$stripped" | grep -q '^[[:space:]]*\[ui\.sidebar\.spaces\]'; then
  echo "note: $herdr_config already defines [ui.sidebar.spaces]; merge these rows by hand:"
  printf '%s\n' "$herdr_block"
else
  printf '%s\n\n%s\n' "$stripped" "$herdr_block" | sed '/./,$!d' > "$herdr_config.tmp"
  mv "$herdr_config.tmp" "$herdr_config"
  echo "configured herdr sidebar in $herdr_config"
  if command -v herdr >/dev/null 2>&1; then
    herdr server reload-config >/dev/null 2>&1 || true
  fi
fi
