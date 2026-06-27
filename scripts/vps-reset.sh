#!/usr/bin/env bash
set -euo pipefail

YES=0
DRY_RUN=1
REBOOT=0

usage() {
  cat <<'EOF'
Usage: sudo bash scripts/vps-reset.sh [--dry-run] [--yes] [--reboot]

Destructively resets an Ubuntu 22.04/24.04 VPS for Opsi manual deploy tests.
Default is --dry-run. Real deletion requires --yes.

Removes K3s/containerd runtime state, CNI/kubelet state, and Opsi config/data.
Preserves SSH, users, firewall, package cache, repo checkout, Git, and Go.
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
    --reboot)
      REBOOT=1
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

echo "Opsi VPS reset"
echo "mode: $([ "$DRY_RUN" -eq 1 ] && echo dry-run || echo destructive)"
echo "target: ${PRETTY_NAME:-Linux systemd}"
echo
echo "Will remove:"
cat <<'EOF'
- opsi-agent systemd state
- K3s server/agent install and runtime state
- K3s containerd/CNI/kubelet state
- Opsi config/data under /etc/opsi, /var/lib/opsi, /opt/opsi
- Opsi temp build/cache files under /tmp
EOF
echo

if [ "$YES" -ne 1 ] && [ "$DRY_RUN" -ne 1 ]; then
  echo "refusing destructive reset without --yes" >&2
  exit 2
fi

stop_disable_service opsi-agent.service
stop_disable_service k3s.service
stop_disable_service k3s-agent.service

if [ -x /usr/local/bin/k3s-killall.sh ]; then
  run /usr/local/bin/k3s-killall.sh || true
elif [ "$DRY_RUN" -eq 1 ]; then
  echo "[dry-run] skip missing /usr/local/bin/k3s-killall.sh"
fi

if [ -x /usr/local/bin/k3s-uninstall.sh ]; then
  run /usr/local/bin/k3s-uninstall.sh || true
elif [ "$DRY_RUN" -eq 1 ]; then
  echo "[dry-run] skip missing /usr/local/bin/k3s-uninstall.sh"
fi

if [ -x /usr/local/bin/k3s-agent-uninstall.sh ]; then
  run /usr/local/bin/k3s-agent-uninstall.sh || true
elif [ "$DRY_RUN" -eq 1 ]; then
  echo "[dry-run] skip missing /usr/local/bin/k3s-agent-uninstall.sh"
fi

for path in \
  /etc/rancher/k3s \
  /var/lib/rancher/k3s \
  /var/lib/kubelet \
  /etc/cni/net.d \
  /var/lib/cni \
  /run/k3s \
  /run/flannel \
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

if command -v ctr >/dev/null 2>&1; then
  run ctr --namespace k8s.io images prune || true
  run ctr --namespace k8s.io snapshots cleanup || true
elif [ "$DRY_RUN" -eq 1 ]; then
  echo "[dry-run] skip missing ctr prune"
fi

run systemctl daemon-reload

if [ "$REBOOT" -eq 1 ]; then
  run systemctl reboot
else
  echo
  echo "reset complete. Reboot is recommended before reinstalling K3s:"
  echo "  sudo reboot"
fi
