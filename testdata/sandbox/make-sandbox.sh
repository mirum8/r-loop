#!/bin/sh
set -eu

src="$(cd "$(dirname "$0")" && pwd)"
dest="${1:-$(mktemp -d "${TMPDIR:-/tmp}/r-loop-sandbox-XXXXXX")}"

mkdir -p "$dest"
[ -z "$(ls -A "$dest")" ] || { echo "make-sandbox.sh: $dest is not empty" >&2; exit 1; }
(cd "$src" && tar cf - --exclude make-sandbox.sh --exclude README.md .) | (cd "$dest" && tar xf -)

git -C "$dest" init -q -b main
git -C "$dest" add -A
git -C "$dest" -c user.name=sandbox -c user.email=sandbox@localhost commit -q -m "sandbox baseline"
echo "$dest"
