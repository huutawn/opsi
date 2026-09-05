#!/usr/bin/env bash
set -euo pipefail

YES=0
DRY_RUN=1
usage() {
  cat <<'EOF'
Usage: sudo bash scripts/vps-reset.sh [--dry-run] [--yes]

Removes only Opsi-owned bootstrap, Agent, credential, and (when proven Opsi-owned)
K3s runtime state from an Ubuntu 22.04/24.04 VPS used for Opsi tests.
Default is --dry-run. Real deletion requires --yes.

It never resets or reboots the machine. It preserves SSH host keys, users,
firewall, packages, unrelated Docker/container images, repo checkout, Git, and Go.
K3s is removed only when an Opsi ownership marker or Opsi Agent configuration is present.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --yes)
      YES=1
      DRY_RUN=0
      ;;
    --dry-run)
      DRY_RUN=1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

if [ "$(id -u)" -ne 0 ]; then
  echo "must run as root: sudo bash scripts/vps-reset.sh --dry-run" >&2
  exit 1
fi

if [ "$(uname -s)" != "Linux" ]; then
  echo "unsupported OS: Linux required" >&2
  exit 1
fi

if ! command -v systemctl >/dev/null 2>&1; then
  echo "unsupported init: systemd required" >&2
  exit 1
fi

if [ -r /etc/os-release ]; then
  . /etc/os-release
  if [ "${ID:-}" != "ubuntu" ]; then
    echo "unsupported distro: Ubuntu 22.04/24.04 expected, got ${PRETTY_NAME:-unknown}" >&2
    exit 1
  fi
fi

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '[dry-run] %q' "$1"
    shift
    for arg in "$@"; do
      printf ' %q' "$arg"
    done
    printf '\n'
    return 0
  fi
  "$@"
}

remove_path() {
  local path="$1"
  if [ -e "$path" ] || [ -L "$path" ]; then
    run rm -rf "$path"
  elif [ "$DRY_RUN" -eq 1 ]; then
    echo "[dry-run] skip missing $path"
  fi
}

stop_disable_service() {
  local name="$1"
  if systemctl list-unit-files "$name" >/dev/null 2>&1 || systemctl status "$name" >/dev/null 2>&1; then
    run systemctl stop "$name" || true
    run systemctl disable "$name" || true
  elif [ "$DRY_RUN" -eq 1 ]; then
    echo "[dry-run] skip missing service $name"
  fi
}

owns_opsi_runtime() {
  [ -f /etc/opsi/agent.yaml ] || \
    [ -f /etc/systemd/system/opsi-agent.service ] || \
    [ -f /var/lib/opsi/swap.marker ]
}

echo "Opsi VPS cleanup"
echo "mode: $([ "$DRY_RUN" -eq 1 ] && echo dry-run || echo destructive)"
echo "target: ${PRETTY_NAME:-Linux systemd}"
echo
echo "Will remove:"
cat <<'EOF'
- opsi-agent systemd state
- K3s server/agent install and runtime state, only when Opsi ownership is proven
- Opsi config/data under /etc/opsi, /var/lib/opsi, /opt/opsi
- Opsi canonical swap (/var/lib/opsi/swapfile) and /etc/fstab entry
- Opsi temp build/cache files under /tmp
EOF
echo

if [ "$YES" -ne 1 ] && [ "$DRY_RUN" -ne 1 ]; then
  echo "refusing destructive reset without --yes" >&2
  exit 2
fi

stop_disable_service opsi-agent.service

OPSI_OWNS_K3S=0
if owns_opsi_runtime; then
  OPSI_OWNS_K3S=1
  stop_disable_service k3s.service
  stop_disable_service k3s-agent.service
  if [ -x /usr/local/bin/k3s-killall.sh ]; then
    run /usr/local/bin/k3s-killall.sh || true
  fi
  if [ -x /usr/local/bin/k3s-uninstall.sh ]; then
    run /usr/local/bin/k3s-uninstall.sh || true
  fi
  if [ -x /usr/local/bin/k3s-agent-uninstall.sh ]; then
    run /usr/local/bin/k3s-agent-uninstall.sh || true
  fi
else
  echo "Opsi ownership marker absent: preserving any existing K3s installation."
fi

if command -v swapon >/dev/null 2>&1 && command -v swapoff >/dev/null 2>&1; then
  if swapon --show=NAME --noheadings 2>/dev/null | grep -q '^/var/lib/opsi/swapfile$'; then
    run swapoff /var/lib/opsi/swapfile
  elif [ "$DRY_RUN" -eq 1 ]; then
    echo "[dry-run] skip swapoff (not active)"
  fi
fi

if [ -f /etc/fstab ] && grep -q '^[[:space:]]*/var/lib/opsi/swapfile[[:space:]]' /etc/fstab; then
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "[dry-run] remove /var/lib/opsi/swapfile entry from /etc/fstab"
  else
    sed -i '/^[[:space:]]*\/var\/lib\/opsi\/swapfile[[:space:]]/d' /etc/fstab
  fi
fi
for path in \
  /etc/opsi \
  /var/lib/opsi \
  /opt/opsi \
  /tmp/opsi-builds \
  /tmp/opsi-agent.sqlite \
  /tmp/opsi-manual-phase2.sqlite \
  /tmp/opsi-manual-phase3.sqlite \
  /tmp/opsi-sync-state.json; do
  remove_path "$path"
done

if [ "$OPSI_OWNS_K3S" -eq 1 ]; then
  for path in /etc/rancher/k3s /var/lib/rancher/k3s /var/lib/kubelet /etc/cni/net.d /var/lib/cni /run/k3s /run/flannel; do
    remove_path "$path"
  done
fi

run systemctl daemon-reload

echo
echo "Opsi cleanup complete. The VPS was not rebooted or otherwise reset."
