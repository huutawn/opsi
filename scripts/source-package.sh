#!/usr/bin/env bash
set -euo pipefail

umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(git -C "$SCRIPT_DIR/.." rev-parse --show-toplevel)"
PRIVATE_KEY_PATTERN='-----BEGIN (PRIVATE KEY|ENCRYPTED PRIVATE KEY|RSA PRIVATE KEY|EC PRIVATE KEY|DSA PRIVATE KEY|OPENSSH PRIVATE KEY)-----'
# Covered markers include BEGIN PRIVATE KEY, BEGIN RSA PRIVATE KEY,
# BEGIN EC PRIVATE KEY, BEGIN DSA PRIVATE KEY, and BEGIN OPENSSH PRIVATE KEY.
REQUIRED_SOURCE_PATHS=(
  Makefile
  agent/go.mod
  cli/go.mod
  cloud/go.mod
  contracts/go/go.mod
  contracts/agent/v1/status.proto
)
REQUIRED_RELEASE_PATHS=(
  opsi
  opsi-agent
  opsi-cloud
  opsi-bootstrap-worker
  checksums.txt
  config.examples/agent.config.example.yaml
  config.examples/cloud.config.example.json
  docs/demo_runbook.md
)
TEMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/opsi-source-package.XXXXXX")"
TEMP_ARCHIVE=""

cleanup() {
  rm -rf -- "$TEMP_ROOT"
  if [[ -n "$TEMP_ARCHIVE" ]]; then
    rm -f -- "$TEMP_ARCHIVE"
  fi
}
trap cleanup EXIT

is_env_example() {
  local base="${1##*/}"
  [[ "$base" == ".env.example" || "$base" == ".env.sample" ]]
}

is_safe_source_secrets_path() {
  local path="${1#./}"
  # This directory contains UI source for secret management, not secret files.
  [[ "$path" == "cli/ui/features/secrets" || "$path" == cli/ui/features/secrets/* ]]
}

is_forbidden_source_path() {
  local path="${1#./}" base="${1##*/}"

  if is_env_example "$path"; then
    return 1
  fi

  case "$path" in
    config.local.*|*/config.local.*)
      return 0
      ;;
    .env|*/.env|.env.*|*/.env.*)
      return 0
      ;;
    secrets|secrets/*|*/secrets|*/secrets/*)
      if ! is_safe_source_secrets_path "$path"; then
        return 0
      fi
      ;;
    certs|certs/*|*/certs|*/certs/*|node_modules|node_modules/*|*/node_modules|*/node_modules/*|.next|.next/*|*/.next|*/.next/*|bin|bin/*|*/bin|*/bin/*|release|release/*|*/release|*/release/*|dist|dist/*|*/dist|*/dist/*|out|out/*|*/out|*/out/*|coverage|coverage/*|*/coverage|*/coverage/*|.tmp|.tmp/*|*/.tmp|*/.tmp/*|tmp|tmp/*|*/tmp|*/tmp/*|.cache|.cache/*|*/.cache|*/.cache/*)
      return 0
      ;;
    id_rsa|*/id_rsa|id_ed25519|*/id_ed25519|kubeconfig|*/kubeconfig|kubeconfig.*|*/kubeconfig.*)
      return 0
      ;;
  esac

  case "$base" in
    *.key|*.p12|*.pfx|*.jks|*.db|*.db-*|*.sqlite|*.sqlite3|*.sqlite-*|*.log|*.log.*|*.test|*.out|*.coverprofile|*.prof|*.tsbuildinfo)
      return 0
      ;;
    opsi|opsi-agent|opsi-cloud|opsi-bootstrap-worker)
      return 0
      ;;
    *.tar|*.tar.gz|*.tgz|*.zip)
      return 0
      ;;
  esac

  return 1
}

normalize_relative_path() {
  local input="$1" part joined="" index
  local -a parts stack=()
  IFS='/' read -r -a parts <<< "$input"
  for part in "${parts[@]}"; do
    case "$part" in
      ''|.)
        ;;
      ..)
        if ((${#stack[@]} == 0)); then
          return 1
        fi
        index=$((${#stack[@]} - 1))
        unset 'stack[index]'
        ;;
      *)
        stack+=("$part")
        ;;
    esac
  done
  if ((${#stack[@]} > 0)); then
    joined="$(IFS=/; printf '%s' "${stack[*]}")"
  fi
  printf '%s\n' "$joined"
}

check_symlink_within() {
  local base="$1" path="$2" target parent normalized base_real resolved
  target="$(readlink -- "$base/$path")"
  if [[ "$target" == /* || "$target" == [A-Za-z]:[\\/]* || "$target" == *\\* || "$target" == *$'\n'* || "$target" == *$'\r'* ]]; then
    printf 'unsafe symlink: %s\n' "$path" >&2
    return 1
  fi
  parent="${path%/*}"
  if [[ "$parent" == "$path" ]]; then
    parent="."
  fi
  if ! normalized="$(normalize_relative_path "$parent/$target")"; then
    printf 'unsafe symlink: %s\n' "$path" >&2
    return 1
  fi
  if [[ -z "$normalized" ]]; then
    printf 'unsafe symlink: %s\n' "$path" >&2
    return 1
  fi
  base_real="$(realpath -e -- "$base")"
  resolved="$(realpath -m -- "$base/$path")"
  if [[ "$resolved" != "$base_real" && "$resolved" != "$base_real"/* ]]; then
    printf 'unsafe symlink: %s\n' "$path" >&2
    return 1
  fi
}

candidate_file_list() {
  local output="$1" excluded="${2:-}" path absolute
  : > "$output"
  (
    cd "$ROOT"
    git ls-files -z --cached --others --exclude-standard
  ) |
    LC_ALL=C sort -z |
    while IFS= read -r -d '' path; do
      absolute="$ROOT/$path"
      if [[ ! -e "$absolute" && ! -L "$absolute" ]]; then
        continue
      fi
      if [[ -n "$excluded" && "$absolute" == "$excluded" ]]; then
        continue
      fi
      printf '%s\0' "$path"
    done > "$output"
}

check_tree() {
  local temporary list path failed=0
  temporary="$(mktemp -d "$TEMP_ROOT/tree.XXXXXX")"
  list="$temporary/candidates"
  candidate_file_list "$list"

  while IFS= read -r -d '' path; do
    if [[ "$path" == /* || "$path" == [A-Za-z]:[\\/]* || "$path" == *\\* || "$path" == *$'\n'* || "$path" == *$'\r'* ]] || archive_path_has_parent_segment "$path"; then
      printf 'forbidden path: %s\n' "$path" >&2
      failed=1
      continue
    fi
    if is_forbidden_source_path "$path"; then
      printf 'forbidden path: %s\n' "$path" >&2
      failed=1
      continue
    fi
    if [[ -L "$ROOT/$path" ]]; then
      if ! check_symlink_within "$ROOT" "$path"; then
        failed=1
      fi
      continue
    fi
    if [[ ! -f "$ROOT/$path" ]]; then
      printf 'unsupported source path: %s\n' "$path" >&2
      failed=1
      continue
    fi
    if LC_ALL=C grep -a -q -E -- "$PRIVATE_KEY_PATTERN" "$ROOT/$path"; then
      printf 'private key material found: %s\n' "$path" >&2
      failed=1
    fi
  done < "$list"

  return "$failed"
}

archive_records() {
  local archive="$1" records="$2" key_paths="$3"
  python3 - "$archive" "$records" "$key_paths" <<'PY'
import re
import sys
import tarfile

archive, records_path, key_paths_path = sys.argv[1:]
markers = re.compile(
    rb"-----BEGIN (?:PRIVATE KEY|ENCRYPTED PRIVATE KEY|RSA PRIVATE KEY|EC PRIVATE KEY|DSA PRIVATE KEY|OPENSSH PRIVATE KEY)-----"
)

with tarfile.open(archive, "r:*") as source, open(records_path, "wb") as records, open(key_paths_path, "wb") as key_paths:
    for member in source.getmembers():
        if member.isfile():
            kind = b"f"
        elif member.isdir():
            kind = b"d"
        elif member.issym():
            kind = b"s"
        else:
            kind = b"x"
        records.write(kind + b"\0" + member.name.encode("utf-8", "surrogateescape") + b"\0")
        records.write(member.linkname.encode("utf-8", "surrogateescape") + b"\0")
        if not member.isfile():
            continue
        extracted = source.extractfile(member)
        if extracted is None:
            continue
        tail = b""
        found = False
        while True:
            chunk = extracted.read(1024 * 1024)
            if not chunk:
                break
            data = tail + chunk
            if markers.search(data):
                found = True
                break
            tail = data[-128:]
        if found:
            key_paths.write(member.name.encode("utf-8", "surrogateescape") + b"\0")
PY
}

archive_path_has_parent_segment() {
  local path="$1" part
  local -a parts
  IFS='/' read -r -a parts <<< "$path"
  for part in "${parts[@]}"; do
    if [[ "$part" == ".." ]]; then
      return 0
    fi
  done
  return 1
}

check_archive() {
  local archive="$1" temporary records key_paths kind name link relative parent normalized path failed=0
  local required
  declare -A found=()

  if [[ ! -f "$archive" || ! -r "$archive" || ! -s "$archive" ]]; then
    printf 'archive missing or unreadable: %s\n' "$archive" >&2
    return 1
  fi
  if ! gzip -t -- "$archive"; then
    printf 'archive unreadable: %s\n' "$archive" >&2
    return 1
  fi

  temporary="$(mktemp -d "$TEMP_ROOT/archive.XXXXXX")"
  records="$temporary/records"
  key_paths="$temporary/key-paths"
  if ! archive_records "$archive" "$records" "$key_paths"; then
    printf 'archive unreadable: %s\n' "$archive" >&2
    return 1
  fi

  while IFS= read -r -d '' kind && IFS= read -r -d '' name && IFS= read -r -d '' link; do
    if [[ "$name" == /* || "$name" != opsi/* || "$name" == *\\* || "$name" == *$'\n'* || "$name" == *$'\r'* ]] || archive_path_has_parent_segment "$name"; then
      printf 'unsafe archive path: %s\n' "$name" >&2
      failed=1
      continue
    fi
    relative="${name#opsi/}"
    if [[ "$relative" == /* || "$relative" == [A-Za-z]:[\\/]* ]]; then
      printf 'unsafe archive path: %s\n' "$name" >&2
      failed=1
      continue
    fi
    if is_forbidden_source_path "$relative"; then
      printf 'forbidden path: %s\n' "$name" >&2
      failed=1
    fi
    case "$kind" in
      f|d)
        ;;
      s)
        if [[ "$link" == /* || "$link" == [A-Za-z]:[\\/]* || "$link" == *\\* || "$link" == *$'\n'* || "$link" == *$'\r'* ]]; then
          printf 'unsafe symlink: %s\n' "$name" >&2
          failed=1
          continue
        fi
        parent="${name%/*}"
        if ! normalized="$(normalize_relative_path "$parent/$link")" || [[ "$normalized" != opsi && "$normalized" != opsi/* ]]; then
          printf 'unsafe symlink: %s\n' "$name" >&2
          failed=1
        fi
        ;;
      *)
        printf 'unsupported archive member: %s\n' "$name" >&2
        failed=1
        ;;
    esac
    found["$relative"]=1
  done < "$records"

  while IFS= read -r -d '' path; do
    printf 'private key material found: %s\n' "$path" >&2
    failed=1
  done < "$key_paths"

  for required in "${REQUIRED_SOURCE_PATHS[@]}"; do
    if [[ -z "${found[$required]:-}" ]]; then
      printf 'required source path missing: opsi/%s\n' "$required" >&2
      failed=1
    fi
  done

  return "$failed"
}

build_archive() {
  local requested="$1" archive parent temporary list output_exclusion
  if [[ "$requested" == /* ]]; then
    archive="$requested"
  else
    archive="$ROOT/$requested"
  fi
  parent="$(dirname "$archive")"
  mkdir -p "$parent"
  archive="$(realpath -m -- "$archive")"
  temporary="$(mktemp -d "$TEMP_ROOT/build.XXXXXX")"
  list="$temporary/candidates"
  output_exclusion="$archive"

  check_tree
  candidate_file_list "$list" "$output_exclusion"
  TEMP_ARCHIVE="$(mktemp "$parent/.$(basename "$archive").tmp.XXXXXX")"
  (
    cd "$ROOT"
    tar --null --no-recursion --hard-dereference --files-from="$list" \
      --owner=0 --group=0 --numeric-owner --sort=name --transform='s,^,opsi/,' -cf -
  ) | gzip -n > "$TEMP_ARCHIVE"
  check_archive "$TEMP_ARCHIVE"
  mv -f -- "$TEMP_ARCHIVE" "$archive"
  TEMP_ARCHIVE=""
  check_archive "$archive"
}

is_forbidden_release_path() {
  local path="$1"
  case "$path" in
    opsi|opsi-agent|opsi-cloud|opsi-bootstrap-worker)
      return 1
      ;;
  esac
  is_forbidden_source_path "$path"
}

check_release() {
  local directory="$1" root path relative failed=0 required
  declare -A found=()

  if [[ ! -d "$directory" ]]; then
    printf 'release directory missing: %s\n' "$directory" >&2
    return 1
  fi
  root="$(cd "$directory" && pwd)"
  while IFS= read -r -d '' path; do
    relative="${path#"$root"/}"
    found["$relative"]=1
    if is_forbidden_release_path "$relative"; then
      printf 'forbidden release path: %s\n' "$relative" >&2
      failed=1
      continue
    fi
    if [[ -L "$path" ]]; then
      if ! check_symlink_within "$root" "$relative"; then
        failed=1
      fi
      continue
    fi
    if [[ ! -f "$path" && ! -d "$path" ]]; then
      printf 'unsupported release path: %s\n' "$relative" >&2
      failed=1
      continue
    fi
    if [[ -f "$path" ]] && LC_ALL=C grep -a -q -E -- "$PRIVATE_KEY_PATTERN" "$path"; then
      printf 'private key material found: %s\n' "$relative" >&2
      failed=1
    fi
  done < <(find "$root" -mindepth 1 -print0 | LC_ALL=C sort -z)

  for required in "${REQUIRED_RELEASE_PATHS[@]}"; do
    if [[ -z "${found[$required]:-}" ]]; then
      printf 'required release path missing: %s\n' "$required" >&2
      failed=1
    fi
  done
  return "$failed"
}

usage() {
  printf 'usage: %s check-tree | build <archive> | check <archive> | check-release <directory> | self-test\n' "$0" >&2
}

if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

case "${1:-}" in
  check-tree)
    [[ "$#" -eq 1 ]] || { usage; exit 2; }
    check_tree
    ;;
  build)
    [[ "$#" -eq 2 ]] || { usage; exit 2; }
    build_archive "$2"
    ;;
  check)
    [[ "$#" -eq 2 ]] || { usage; exit 2; }
    check_archive "$2"
    ;;
  check-release)
    [[ "$#" -eq 2 ]] || { usage; exit 2; }
    check_release "$2"
    ;;
  self-test)
    [[ "$#" -eq 1 ]] || { usage; exit 2; }
    bash "$SCRIPT_DIR/source-package-test.sh"
    ;;
  *)
    usage
    exit 2
    ;;
esac
