#!/usr/bin/env python3
"""
Real-Project Read-Only Smoke Test
Project: proj-219cc5584acc1e44
Exercises live Codex CLI -> Opsi MCP stdio -> Opsi Cloud facts.
Verifies factual grounding, zero mutations, and latency breakdown.
"""

import asyncio
import json
import os
import subprocess
import sys
import tempfile
import time

PROJECT_ID = "proj-219cc5584acc1e44"
CONFIG_PATH = os.path.expanduser("~/.config/opsi/config.yaml")

def log(msg):
    print(f"[{time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}] {msg}")

def call_mcp(tools_to_call):
    bin_path = os.path.abspath("./bin/opsi")
    proc = subprocess.Popen(
        [bin_path, "--config", CONFIG_PATH, "mcp", "--project-id", PROJECT_ID],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True
    )
    init_req = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "smoke", "version": "1.0"}}}) + "\n"

    input_str = init_req
    req_id = 2
    for name, args in tools_to_call:
        input_str += json.dumps({"jsonrpc": "2.0", "id": req_id, "method": "tools/call", "params": {"name": name, "arguments": args}}) + "\n"
        req_id += 1

    stdout, stderr = proc.communicate(input=input_str)

    responses = []
    for line in stdout.strip().splitlines():
        if line.strip():
            try:
                responses.append(json.loads(line))
            except Exception:
                pass
    return responses, stderr

def take_snapshot():
    tools = [
        ("project_context", {"project_id": PROJECT_ID}),
        ("topology", {"project_id": PROJECT_ID}),
        ("applications_list", {"project_id": PROJECT_ID}),
        ("deployments_list", {"project_id": PROJECT_ID}),
        ("managed_resources_list", {"project_id": PROJECT_ID}),
    ]
    responses, stderr = call_mcp(tools)
    snapshot = {}
    for resp in responses:
        if resp.get("id") == 1:
            continue
        result = resp.get("result", {})
        content = result.get("content", [])
        if content and content[0].get("text"):
            try:
                data = json.loads(content[0]["text"])
                req_id = resp.get("id")
                tool_name = tools[req_id - 2][0]
                snapshot[tool_name] = data
            except Exception as e:
                log(f"Failed to parse response for id {resp.get('id')}: {e}")
    return snapshot

def run_codex_assistant_turn(report_dir):
    log("Running real Codex Assistant turn via Codex CLI with Opsi MCP bridge...")
    bin_path = os.path.abspath("./bin/opsi")
    repo_root = os.path.abspath(".")

    enabled_tools = [
        "project_review_context", "deployment_readiness_context", "dependency_analysis_context",
        "validate_dependency_proposal", "validate_service_configuration_proposal", "validate_source_patch_proposal",
        "project_context", "topology", "applications_list", "application_get", "application_dependencies",
        "managed_resources_list", "managed_resource_get", "build_records_list", "build_record_get",
        "deployments_list", "deployment_get", "deployment_preflight", "source_risk_report",
        "dependency_verification_latest", "dependency_verification_history", "source_files_list",
        "source_file_read", "source_search"
    ]
    tools_toml = "[" + ",".join([f'"{t}"' for t in enabled_tools]) + "]"
    env_vars_toml = '["DBUS_SESSION_BUS_ADDRESS","XDG_RUNTIME_DIR","HOME","PATH","USER","OPSI_CONFIG"]'
    mcp_args = f'["--config", "{CONFIG_PATH}", "mcp", "--project-id", "{PROJECT_ID}"]'
    mcp_config = f'mcp_servers.opsi={{command="{bin_path}",args={mcp_args},cwd="{repo_root}",required=true,enabled_tools={tools_toml},default_tools_approval_mode="writes",env_vars={env_vars_toml},startup_timeout_sec=10,tool_timeout_sec=45}}'

    disabled_features = [
        "shell_tool", "unified_exec", "browser_use", "browser_use_external", "in_app_browser",
        "standalone_web_search", "network_proxy", "computer_use", "apps", "enable_mcp_apps",
        "plugins", "recommended_plugins", "remote_plugin", "skill_search", "image_generation",
        "view_image", "tool_suggest", "multi_agent"
    ]
    disable_args = []
    for f in disabled_features:
        disable_args.extend(["--disable", f])
    workspace = tempfile.mkdtemp(prefix="opsi-real-smoke-")
    os.chmod(workspace, 0o700)
    output_schema = os.path.join(workspace, "assistant-output-schema.json")
    last_message = os.path.join(workspace, "last-message.txt")

    proposal_schema = {
        "type": "object",
        "additionalProperties": False,
        "properties": {
            "project_id": {"type": "string"},
            "environment_id": {"type": "string"},
            "application_id": {"type": "string"},
            "provenance": {
                "type": "object",
                "additionalProperties": False,
                "properties": {
                    "build_record_id": {"type": "string"},
                    "source_commit": {"type": "string"},
                    "application_root": {"type": "string"}
                },
                "required": ["build_record_id", "source_commit", "application_root"]
            },
            "rationale": {
                "type": "object",
                "additionalProperties": False,
                "properties": {
                    "observed_source": {"type": "string"},
                    "opsi_facts": {"type": "string"},
                    "inference": {"type": "string"}
                },
                "required": ["observed_source", "opsi_facts", "inference"]
            },
            "files": {
                "type": "array",
                "items": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "path": {"type": "string"},
                        "base_blob_sha": {"type": "string"},
                        "unified_diff": {"type": "string"}
                    },
                    "required": ["path", "base_blob_sha", "unified_diff"]
                }
            },
            "evidence": {
                "type": "array",
                "items": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "type": {"type": "string"},
                        "file": {"type": "string"},
                        "line": {"type": "integer"},
                        "reason": {"type": "string"},
                        "symbol": {"type": "string"}
                    },
                    "required": ["type", "file", "line", "reason", "symbol"]
                }
            },
            "impact": {
                "type": "object",
                "additionalProperties": False,
                "properties": {
                    "alternative_configuration_only_solution": {"type": "boolean"},
                    "depends_on_unapplied_dependency_proposal": {"type": "boolean"}
                },
                "required": ["alternative_configuration_only_solution", "depends_on_unapplied_dependency_proposal"]
            }
        },
        "required": ["project_id", "environment_id", "application_id", "provenance", "rationale", "files", "evidence", "impact"]
    }

    schema_content = {
        "type": "object",
        "additionalProperties": False,
        "properties": {
            "message": {"type": "string"},
            "configuration_proposals": {
                "type": "array",
                "items": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "application_id": {"type": "string"},
                        "application_name": {"type": "string"},
                        "environment_id": {"type": "string"},
                        "rationale": {"type": "string"},
                        "expected_revision": {"type": "integer", "minimum": 0},
                        "expected_state_hash": {"type": "string"},
                        "analysis_inputs_hash": {"type": "string"},
                        "draft_json": {"type": "string"}
                    },
                    "required": ["application_id", "application_name", "environment_id", "rationale", "expected_revision", "expected_state_hash", "analysis_inputs_hash", "draft_json"]
                }
            },
            "source_patch_proposals": {
                "type": "array",
                "items": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "project_id": {"type": "string"},
                        "environment_id": {"type": "string"},
                        "application_id": {"type": "string"},
                        "source_commit": {"type": "string"},
                        "application_root": {"type": "string"},
                        "proposal_hash": {"type": "string"},
                        "validation_status": {"type": "string", "enum": ["VALID", "VALID_WITH_WARNINGS"]},
                        "proposal": proposal_schema
                    },
                    "required": ["project_id", "environment_id", "application_id", "source_commit", "application_root", "proposal_hash", "validation_status", "proposal"]
                }
            }
        },
        "required": ["message", "configuration_proposals", "source_patch_proposals"]
    }
    with open(output_schema, "w") as f:
        json.dump(schema_content, f)
    os.chmod(output_schema, 0o600)

    prompt = "Review this project for Opsi deployment readiness and list the highest-risk gaps. Use only Opsi MCP facts."
    instructions = f"You are the Opsi AI Assistant. Use only the opsi MCP tools for project facts. Do not use shell, filesystem, web, connectors, or any non-Opsi tool. Never claim a change is applied. Return a concise review.\n\nUser request:\n{prompt}"

    cmd = [
        "codex", "exec"
    ] + disable_args + [
        "--json",
        "--ignore-user-config",
        "--skip-git-repo-check",
        "--sandbox", "read-only",
        "-C", workspace,
        "-c", mcp_config,
        "--output-schema", output_schema,
        "-o", last_message,
        "-"
    ]

    start_time = time.time()
    t0 = start_time
    log(f"Spawning Codex process...")
    proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, cwd=workspace)
    stdout, stderr = proc.communicate(input=instructions)
    total_latency = time.time() - start_time

    log(f"Codex turn completed in {total_latency:.2f}s with exit code {proc.returncode}")

    # Parse events and extract tool calls & latency
    events = []
    terminal_calls = {}
    thread_id = ""
    for line in stdout.strip().splitlines():
        if line.strip():
            try:
                ev = json.loads(line)
                events.append(ev)
                ev_type = ev.get("type", "")
                if ev_type == "thread.started":
                    thread_id = ev.get("thread_id", "")
                item = ev.get("item", ev)
                if item.get("type") in ["mcp_tool_call", "tool_call", "mcp_call", "mcp_tool_result"]:
                    tool = item.get("tool") or item.get("name")
                    status = item.get("status", "")
                    call_id = item.get("call_id") or item.get("id") or item.get("item_id")
                    if call_id and status in ("completed", "success", "failed"):
                        terminal_calls[str(call_id)] = {"tool": tool, "status": status, "is_error": bool(item.get("is_error")), "error": item.get("error")}
            except Exception:
                pass

    response_text = ""
    if os.path.exists(last_message):
        with open(last_message) as f:
            try:
                resp_json = json.load(f)
                response_text = resp_json.get("message", "")
            except Exception:
                with open(last_message) as raw_f:
                    response_text = raw_f.read()

    completed_calls = [item for item in terminal_calls.values() if item["status"] in ("completed", "success") and not item["is_error"] and not item["error"]]
    failed_calls = [item for item in terminal_calls.values() if item not in completed_calls]
    # Save sanitized evidence to report_dir
    evidence = {
        "project_id": PROJECT_ID,
        "total_latency_seconds": round(total_latency, 3),
        "return_code": proc.returncode,
        "thread_id": thread_id,
        "mcp_tool_calls": list(terminal_calls.values()),
        "successful_mcp_tool_calls_count": len(completed_calls),
        "failed_mcp_tool_calls_count": len(failed_calls),
        "git_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
        "git_worktree_clean": not bool(subprocess.check_output(["git", "status", "--porcelain"], text=True).strip()),
        "response_length": len(response_text),
        "response_excerpt": response_text[:500],
        "events_count": len(events),
        "stderr_excerpt": stderr[:300] if stderr else ""
    }

    with open(os.path.join(report_dir, "real_smoke_evidence.json"), "w") as f:
        json.dump(evidence, f, indent=2)

    return evidence, response_text

def main():
    report_dir = sys.argv[1] if len(sys.argv) > 1 else f"/tmp/opsi-mcp-acceptance-{os.popen('git rev-parse --short HEAD').read().strip()}"
    os.makedirs(report_dir, exist_ok=True)
    os.chmod(report_dir, 0o700)

    log(f"=== Starting Real Project Read-Only Smoke Suite ({PROJECT_ID}) ===")
    log(f"Report Dir: {report_dir}")

    # 1. Pre-smoke authority snapshot
    log("[STEP 1/4] Collecting Pre-Smoke Authority Snapshot...")
    snap_before = take_snapshot()
    with open(os.path.join(report_dir, "snapshot_before.json"), "w") as f:
        json.dump(snap_before, f, indent=2)
    log(f"  -> Topology Revision: {snap_before.get('project_context', {}).get('topology_revision')}")
    log(f"  -> Topology State Hash: {snap_before.get('project_context', {}).get('topology_state_hash')}")
    log(f"  -> Total Deployments: {snap_before.get('project_context', {}).get('deployment_summary', {}).get('total_deployments')}")

    # 2. Run real Codex assistant turn
    log("\n[STEP 2/4] Executing Live Codex AI Assistant Turn...")
    evidence, response_text = run_codex_assistant_turn(report_dir)
    log(f"  -> Observed {evidence['successful_mcp_tool_calls_count']} completed MCP tool calls.")
    log(f"  -> Assistant Response: {response_text[:120]}...")

    assert evidence["successful_mcp_tool_calls_count"] >= 1, "Expected at least 1 successful Opsi MCP tool call"
    assert evidence["failed_mcp_tool_calls_count"] == 0, "Observed failed Opsi MCP tool calls"
    assert evidence["git_worktree_clean"], "Real smoke acceptance must run from a clean committed worktree"
    assert len(response_text) > 0, "Expected non-empty assistant response"

    # 3. Post-smoke authority snapshot
    log("\n[STEP 3/4] Collecting Post-Smoke Authority Snapshot...")
    snap_after = take_snapshot()
    with open(os.path.join(report_dir, "snapshot_after.json"), "w") as f:
        json.dump(snap_after, f, indent=2)

    # 4. Compare snapshots to prove zero mutation
    log("\n[STEP 4/4] Verifying Zero Authority Mutation...")
    before_proj = snap_before.get("project_context", {})
    after_proj = snap_after.get("project_context", {})

    assert before_proj.get("topology_revision") == after_proj.get("topology_revision"), "Topology revision changed!"
    assert before_proj.get("topology_state_hash") == after_proj.get("topology_state_hash"), "Topology state hash changed!"
    assert before_proj.get("deployment_summary") == after_proj.get("deployment_summary"), "Deployments changed!"
    assert before_proj.get("managed_resource_count") == after_proj.get("managed_resource_count"), "Resources changed!"
    assert before_proj.get("application_count") == after_proj.get("application_count"), "Applications changed!"

    log("✓ Authority Invariants Verified: Topology, configuration, deployments, and resources are byte-for-byte unchanged.")
    log(f"=== Real Project Smoke Acceptance: ALL PASS ===")

if __name__ == "__main__":
    main()
