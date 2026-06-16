#!/usr/bin/env bash

set -euo pipefail
shopt -s extglob

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )

# Go to sanitizer root directory
cd "$SCRIPT_DIR/.."

KERNEL="${KERNEL:-kernel}"
U_ROOT="${U_ROOT:-u-root}"
U_ROOT_PKG="$(go list -m -f "{{ .Dir }}" github.com/u-root/u-root)"

KERNEL_MODULES=()
ADDITIONAL_U_ROOT_OPTS=()
KERNEL_VERSION=""

while [[ $# -gt 0 ]]; do
  case $1 in
  --kernel-version|-k)
    KERNEL_VERSION="$2"
    shift
    shift
    ;;
  --kernel-module|-m)
    KERNEL_MODULES+=("$2")
    shift
    shift
    ;;
  -o)
    ADDITIONAL_U_ROOT_OPTS+=("-o=$2")
    shift
    shift
    ;;
  -*|--*)
    echo "Unknown option $1"
    exit 1
    ;;
  *)
    echo "No positional arguments allowed"
    exit 1
    ;;
  esac
done

if [[ "$KERNEL_VERSION" == "" ]]; then
  echo "Must specify --kernel-version"
  exit 1
fi

KERNEL_ASSETS="$("$KERNEL" use "$KERNEL_VERSION" --dir ./bin -f path)"

LOAD_CMD=""

if [[ ${#KERNEL_MODULES[@]} -gt 0 ]]; then
  for KERNEL_MODULE in "${KERNEL_MODULES[@]}"; do
    FILES=( "${KERNEL_ASSETS}/modules/${KERNEL_MODULE}".ko?(.xz) )
    if [[ ${#FILES[@]} -eq 0 || ! -e ${FILES[0]} ]]; then
      echo "No module file found for $KERNEL_MODULE"
      exit 1
    fi

    FILE="${FILES[0]}"
    FILENAME="$(basename "$FILE")"
    INITRAMFS_PATH="modules/$FILENAME"
    ADDITIONAL_U_ROOT_OPTS+=("-files=$FILE:$INITRAMFS_PATH")

    LOAD_CMD="${LOAD_CMD}insmod /$INITRAMFS_PATH && "
  done
fi

GOOS=linux CGO_ENABLED=0 "$U_ROOT" \
  -uinitcmd="gosh -c '${LOAD_CMD}sanitizer'" \
  ${ADDITIONAL_U_ROOT_OPTS[@]+"${ADDITIONAL_U_ROOT_OPTS[@]}"} \
  "$U_ROOT_PKG"/cmds/core/init \
  "$U_ROOT_PKG"/cmds/core/gosh \
  "$U_ROOT_PKG"/cmds/core/insmod \
  ./cmd/sanitizer
