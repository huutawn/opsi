#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=source-package.sh
source "$SCRIPT_DIR/source-package.sh"

fixture_tree() {
  local root="$1" path
  for path in "${REQUIRED_SOURCE_PATHS[@]}"; do
    mkdir -p "$root/opsi/$(dirname "$path")"
    printf 'fixture\n' > "$root/opsi/$path"
  done
  mkdir -p "$root/opsi/agent"
  printf 'endpoint: example.invalid\ncredential: CHANGE_ME\n' > "$root/opsi/agent/config.example.yaml"
}

fixture_archive() {
  local root="$1" archive="$2"
  (
    cd "$root"
    find opsi \( -type f -o -type l \) -print0 | LC_ALL=C sort -z |
      tar --null --no-recursion --files-from=- -cf -
  ) | gzip -n > "$archive"
}

fixture_single_member_archive() {
  local archive="$1" member_name="$2"
  python3 - "$archive" "$member_name" <<'PY'
import io
import sys
import tarfile

archive_path, member_name = sys.argv[1:]
payload = b"fixture\n"
with tarfile.open(archive_path, "w:gz") as archive:
    member = tarfile.TarInfo(member_name)
    member.size = len(payload)
    archive.addfile(member, io.BytesIO(payload))
PY
}

expect_archive_failure() {
  local archive="$1" category="$2" output="$3" case_name="$4"
  if check_archive "$archive" > "$output" 2>&1; then
    printf 'self-test expected archive failure: %s\n' "$case_name" >&2
    return 1
  fi
  if ! grep -q -- "$category" "$output"; then
    printf 'self-test wrong archive failure category: %s\n' "$case_name" >&2
    return 1
  fi
}

expect_release_failure() {
  local directory="$1" category="$2" output="$3" case_name="$4"
  if check_release "$directory" > "$output" 2>&1; then
    printf 'self-test expected release failure: %s\n' "$case_name" >&2
    return 1
  fi
  if ! grep -q -- "$category" "$output"; then
    printf 'self-test wrong release failure category: %s\n' "$case_name" >&2
    return 1
  fi
}

write_fixture_file() {
  local root="$1" path="$2"
  mkdir -p "$root/$(dirname "$path")"
  printf 'fixture\n' > "$root/$path"
}

test_candidate_selection() {
  local temporary="$1" list excluded_list path example_found=0
  list="$temporary/candidates"
  excluded_list="$temporary/candidates-excluded"
  candidate_file_list "$list"
  while IFS= read -r -d '' path; do
    case "$path" in
      deploy/dev-control-plane/.env|deploy/dev-control-plane/secrets/*)
        printf 'ignored runtime path entered candidate set: %s\n' "$path" >&2
        return 1
        ;;
      deploy/dev-control-plane/.env.example)
        example_found=1
        ;;
    esac
  done < "$list"
  if [[ -f "$ROOT/deploy/dev-control-plane/.env.example" && "$example_found" -ne 1 ]]; then
    printf 'safe environment example missing from candidate set\n' >&2
    return 1
  fi
  candidate_file_list "$excluded_list" "$ROOT/Makefile"
  while IFS= read -r -d '' path; do
    if [[ "$path" == "Makefile" ]]; then
      printf 'explicit archive output exclusion was not applied\n' >&2
      return 1
    fi
  done < "$excluded_list"
}

test_forbidden_archive_paths() {
  local fixture="$1" archive="$2" output="$3" path
  local -a paths=(
    .env
    .env.production
    agent/config.local.json
    secrets/runtime.txt
    certs/runtime.crt
    agent/runtime.key
    agent/runtime.p12
    agent/runtime.pfx
    agent/runtime.jks
    agent/kubeconfig
    agent/runtime.db
    agent/runtime.sqlite3
    agent/runtime.log
    cli/ui/node_modules/module.js
    cli/ui/.next/cache.bin
    cli/ui/out/index.html
    dist/opsi-source.tar.gz
  )
  for path in "${paths[@]}"; do
    write_fixture_file "$fixture/opsi" "$path"
    fixture_archive "$fixture" "$archive"
    expect_archive_failure "$archive" 'forbidden path:' "$output" "$path"
    rm -f "$fixture/opsi/$path"
  done
}

test_archive_content_markers() {
  local fixture="$1" archive="$2" output="$3" marker="$fixture/opsi/test/key-marker.txt"
  mkdir -p "$(dirname "$marker")"
  printf '%s%s\n' '-----BEGIN ' 'PRIVATE KEY-----' > "$marker"
  fixture_archive "$fixture" "$archive"
  expect_archive_failure "$archive" 'private key material found:' "$output" 'fake PEM private-key marker'
  printf '%s%s\n' '-----BEGIN ' 'OPENSSH PRIVATE KEY-----' > "$marker"
  fixture_archive "$fixture" "$archive"
  expect_archive_failure "$archive" 'private key material found:' "$output" 'fake OpenSSH private-key marker'
  rm -f "$marker"
}

test_archive_member_safety() {
  local fixture="$1" archive="$2" output="$3"
  fixture_single_member_archive "$archive" '../escape'
  expect_archive_failure "$archive" 'unsafe archive path:' "$output" 'traversal path'
  fixture_single_member_archive "$archive" '/absolute'
  expect_archive_failure "$archive" 'unsafe archive path:' "$output" 'absolute path'

  ln -s ../../outside "$fixture/opsi/escaping-link"
  fixture_archive "$fixture" "$archive"
  expect_archive_failure "$archive" 'unsafe symlink:' "$output" 'escaping symlink'
  rm -f "$fixture/opsi/escaping-link"
}

test_filesystem_symlink_safety() {
  local temporary="$1" root="$temporary/symlink-root"
  mkdir -p "$root/inside"
  ln -s ../../outside "$root/inside/escaping-link"
  if check_symlink_within "$root" 'inside/escaping-link' > /dev/null 2>&1; then
    printf 'self-test expected filesystem symlink failure\n' >&2
    return 1
  fi
}

fixture_release() {
  local root="$1" path
  for path in "${REQUIRED_RELEASE_PATHS[@]}"; do
    write_fixture_file "$root" "$path"
  done
}

test_release_policy() {
  local temporary="$1" output="$2" release="$temporary/release"
  fixture_release "$release"
  check_release "$release"

  write_fixture_file "$release" '.env'
  expect_release_failure "$release" 'forbidden release path:' "$output" 'release environment file'
  rm -f "$release/.env"

  write_fixture_file "$release" 'secrets/runtime.txt'
  expect_release_failure "$release" 'forbidden release path:' "$output" 'release secret directory'
  rm -rf "$release/secrets"

  mkdir -p "$release/docs"
  printf '%s%s\n' '-----BEGIN ' 'RSA PRIVATE KEY-----' > "$release/docs/key-marker.txt"
  expect_release_failure "$release" 'private key material found:' "$output" 'release private-key marker'
  rm -f "$release/docs/key-marker.txt"
  check_release "$release"
}

self_test() {
  local temporary fixture archive output
  temporary="$(mktemp -d "$TEMP_ROOT/self-test.XXXXXX")"
  fixture="$temporary/fixture"
  archive="$temporary/fixture.tar.gz"
  output="$temporary/check.out"

  test_candidate_selection "$temporary"
  fixture_tree "$fixture"
  fixture_archive "$fixture" "$archive"
  check_archive "$archive"

  write_fixture_file "$fixture/opsi" '.env.example'
  write_fixture_file "$fixture/opsi" '.env.sample'
  fixture_archive "$fixture" "$archive"
  check_archive "$archive"
  rm -f "$fixture/opsi/.env.example"
  rm -f "$fixture/opsi/.env.sample"

  test_forbidden_archive_paths "$fixture" "$archive" "$output"
  test_archive_content_markers "$fixture" "$archive" "$output"
  test_archive_member_safety "$fixture" "$archive" "$output"
  test_filesystem_symlink_safety "$temporary"
  fixture_archive "$fixture" "$archive"
  check_archive "$archive"
  test_release_policy "$temporary" "$output"
  printf 'source package policy tests passed\n'
}

self_test
