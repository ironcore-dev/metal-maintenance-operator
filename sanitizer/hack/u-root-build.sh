#!/usr/bin/env bash

set -euo pipefail
shopt -s extglob

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )

U_ROOT="${U_ROOT:-u-root}"
U_ROOT_PKG="$(go list -m -f "{{ .Dir }}" github.com/u-root/u-root)"
U_ROOT_ARGS=()

while [[ $# -gt 0 ]]; do
  case $1 in
  builtin:*)
    U_ROOT_ARGS+=("$U_ROOT_PKG"/"${1#builtin:}")
    shift
    ;;
  *)
    U_ROOT_ARGS+=("$1")
    shift
    ;;
  esac
done

GOOS=linux CGO_ENABLED=0 "$U_ROOT" \
  ${U_ROOT_ARGS[@]+"${U_ROOT_ARGS[@]}"}
