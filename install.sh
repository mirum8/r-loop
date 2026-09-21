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

case ":$PATH:" in
  *":$prefix:"*) ;;
  *) echo "note: $prefix is not on PATH" ;;
esac

for dep in herdr git; do
  command -v "$dep" >/dev/null 2>&1 || echo "note: $dep is not on PATH; r-loop needs it at run time"
done
