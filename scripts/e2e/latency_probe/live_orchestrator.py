#!/usr/bin/env python3
import json
import os
import signal
import subprocess
import sys
import time
from datetime import datetime, timezone
import hashlib

def run_ssh(cmd, known_hosts_file, key_path, vps_ip="103.252.137.163", ssh_user="tawn"):
    full_cmd = [
        "ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
        "-o", "PreferredAuthentications=publickey", "-o", "PasswordAuthentication=no",
        "-o", "KbdInteractiveAuthentication=no", "-o", "GSSAPIAuthentication=no",
        "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes",
        "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={known_hosts_file}",
        "-i", key_path, f"{ssh_user}@{vps_ip}", cmd
    ]
    res = subprocess.run(full_cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    return res.returncode, res.stdout, res.stderr

def main():
    bench_dir = sys.argv[1] if len(sys.argv) > 1 else "/tmp/opsi_bench"
    known_hosts = os.path.join(bench_dir, "known_hosts")
    key_path = "/home/tawn/.ssh/vps-only"
    vps_ip = "103.252.137.163"
    ssh_user = os.environ.get("OPSI_TEST_VPS_SSH_USER", "tawn")
    run_id = "run-3deec7c04e7a01a597deb5b0afc7d6b0"
    target_url = "https://tcip.opsidev.site"
    ns = "opsi-proj-219cc5584acc1-env-a55c5984ddddf2-be11c2ed7a"

    os.makedirs(bench_dir, exist_ok=True)
    os.chmod(bench_dir, 0o700)
    probe_out = os.path.join(bench_dir, "probe_output")
    os.makedirs(probe_out, exist_ok=True)

    print(f"=== OPSI LIVE BENCHMARK ORCHESTRATOR ===")
    print(f"Bench Dir:  {bench_dir}")
    print(f"Target URL: {target_url}")
    print(f"Run ID:     {run_id}")
    print(f"========================================")

    # 1. Pre-benchmark snapshot
    print("\n[STEP 1/5] Collecting Pre-Benchmark VPS Snapshot...")
    snap_cmd = f"""
    echo "=== NODE ==="
    kubectl get nodes -o wide
    free -b
    swapon --show
    echo "=== PODS ==="
    kubectl get pods -n {ns} -o wide
    echo "=== RESOURCES ==="
    kubectl get pods -n {ns} -o json
    echo "=== CPU_STAT ==="
    for p in /sys/fs/cgroup/kubepods.slice/*/*.scope/cpu.stat; do
      if [ -f "$p" ]; then
        echo "PATH: $p"
        cat "$p"
      fi
    done
    """
    rc, stdout, stderr = run_ssh(snap_cmd, known_hosts, key_path, vps_ip, ssh_user)
    with open(os.path.join(bench_dir, "snapshot_before.txt"), "w") as f:
        f.write(stdout)

    # 2. Start remote observer
    print("\n[STEP 2/5] Starting VPS Background Observer...")
    observer_script = os.path.join(os.path.dirname(__file__), "remote_observer.sh")
    observer_log = os.path.join(bench_dir, "vps_metrics_1s.jsonl")
    observer_out_f = open(observer_log, "w")

    ssh_observer_cmd = [
        "ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
        "-o", "PreferredAuthentications=publickey", "-o", "PasswordAuthentication=no",
        "-o", "KbdInteractiveAuthentication=no", "-o", "GSSAPIAuthentication=no",
        "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes",
        "-o", "StrictHostKeyChecking=yes", "-o", f"UserKnownHostsFile={known_hosts}",
        "-i", key_path, f"{ssh_user}@{vps_ip}", "bash -s"
    ]
    with open(observer_script, "rb") as script_f:
        observer_proc = subprocess.Popen(ssh_observer_cmd, stdin=script_f, stdout=observer_out_f, stderr=subprocess.PIPE, text=True)

    print(f"  -> Observer started with PID {observer_proc.pid}, logging to {observer_log}")
    time.sleep(3)

    # 3. Run live latency probe
    print("\n[STEP 3/5] Launching Go Latency Probe (Live Mode: 1 -> 5 -> 10 VU, 60s/level)...")
    probe_bin = "/tmp/opsi-latency-probe"
    probe_cmd = [
        probe_bin,
        "-url", target_url,
        "-run-id", run_id,
        "-mode", "live",
        "-output-dir", probe_out,
        "-duration", "60s",
        "-warmup", "10",
        "-recovery", "90",
        "-final-recovery", "120",
        "-vu-levels", "1,5,10"
    ]

    start_bench_time = datetime.now(timezone.utc)
    try:
        probe_res = subprocess.run(probe_cmd, text=True)
        probe_rc = probe_res.returncode
    except Exception as e:
        print(f"[ERROR] Probe run exception: {e}")
        probe_rc = 1
    end_bench_time = datetime.now(timezone.utc)

    # 4. Stop observer and collect post-snapshot
    print("\n[STEP 4/5] Stopping VPS Background Observer...")
    try:
        observer_proc.send_signal(signal.SIGTERM)
        observer_proc.wait(timeout=5)
    except Exception:
        observer_proc.kill()
    observer_out_f.close()
    print("  -> Observer process terminated.")

    print("\n[STEP 5/5] Collecting Post-Benchmark VPS Snapshot and Verifying Health...")
    rc, stdout, stderr = run_ssh(snap_cmd, known_hosts, key_path, vps_ip, ssh_user)
    with open(os.path.join(bench_dir, "snapshot_after.txt"), "w") as f:
        f.write(stdout)

    # Verify no dangling processes or port-forwards
    rc, pgrep_out, _ = run_ssh("pgrep -a -f remote_observer || echo 'NO_DANGLING_OBSERVER'", known_hosts, key_path, vps_ip, ssh_user)
    print(f"  -> Dangling check: {pgrep_out.strip()}")

    print("\n[COMPLETE] Live benchmark orchestration finished.")

if __name__ == "__main__":
    main()
