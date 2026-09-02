#!/usr/bin/env python3
"""
Real MCP Client Protocol Acceptance & Evidence Capture
Uses official Python MCP SDK to connect to `opsi mcp` over stdio.
"""

import asyncio
import json
import os
import subprocess
import sys
import tempfile
from mcp.client.stdio import stdio_client, StdioServerParameters
from mcp.client.session import ClientSession

SYNTHETIC_SECRET = "SOURCE_SECRET_123456789"

async def run_mcp_client_suite():
    print("=== Starting Real MCP Client Acceptance Suite ===")

    with tempfile.TemporaryDirectory() as tmp_dir:
        cfg_path = os.path.join(tmp_dir, "config.yaml")
        with open(cfg_path, "w") as f:
            f.write("cloud_url: https://cloud.opsi.dev\n")

        # Build from the checked source on every run; an existing binary may
        # describe an older MCP surface and must not satisfy this protocol test.
        bin_path = os.path.abspath("./bin/opsi")
        subprocess.run(["go", "build", "-o", bin_path, "./cli/cmd/opsi"], check=True)

        # Connect real MCP client over stdio
        env = {
            **os.environ,
            "HOME": tmp_dir,
            "OPSI_CONFIG": cfg_path,
        }
        params = StdioServerParameters(
            command=bin_path,
            args=["--config", cfg_path, "mcp", "--project-id", "proj-test"],
            env=env
        )

        async with stdio_client(params) as (read, write):
            async with ClientSession(read, write) as session:
                # 1. Initialize Handshake (Section 2 & 3)
                init_res = await session.initialize()
                print(f"[1] Real MCP Client Handshake:")
                print(f"    Protocol Version: {init_res.protocolVersion}")
                print(f"    Server: {init_res.serverInfo.name} ({init_res.serverInfo.version})")
                print(f"    Instructions: {init_res.instructions}")
                assert init_res.protocolVersion == "2024-11-05", f"Expected 2024-11-05, got {init_res.protocolVersion}"
                assert init_res.serverInfo.name == "opsi-mcp", f"Expected opsi-mcp, got {init_res.serverInfo.name}"

                # 2. Ping Handshake
                await session.send_ping()
                print("    ✓ Protocol Ping successful")

                # 3. List Tools Discovery (Section 4 & 5)
                tools_res = await session.list_tools()
                print(f"[2] Tool Discovery: {len(tools_res.tools)} tools found")
                assert len(tools_res.tools) == 24, f"Expected 24 tools, got {len(tools_res.tools)}"

                tool_names = [t.name for t in tools_res.tools]
                print(f"    Discovered Tools: {', '.join(tool_names)}")

                # Verify all factual and advisory tools are strictly non-operational.
                mutation_keywords = ["create_", "update_", "delete_", "apply_", "build_start", "execute_", "patch_", "mutate_"]
                for t in tools_res.tools:
                    for kw in mutation_keywords:
                        assert not t.name.startswith(kw), f"Mutation tool found: {t.name}"
                    assert "read-only" in t.description.lower() or "safe" in t.description.lower() or "immutable" in t.description.lower()
                    # Schema & annotations verification
                    assert t.inputSchema is not None
                    assert t.inputSchema.get("type") == "object"
                    annotations = getattr(t, "annotations", None)
                    if annotations is not None:
                        ro = getattr(annotations, "readOnlyHint", None)
                        if ro is None and isinstance(annotations, dict):
                            ro = annotations.get("readOnlyHint")
                        assert ro is True, f"Tool {t.name} missing readOnlyHint: true"
                assert "dependency_analysis_context" in tool_names
                assert "project_review_context" in tool_names
                assert "validate_dependency_proposal" in tool_names
                assert "validate_service_configuration_proposal" in tool_names
                assert "validate_source_patch_proposal" in tool_names
                print("    ✓ All 24 tools verified strictly non-operational with typed schemas")

                # 4. Unauthenticated Cloud-Authority Tools (Section 8)
                print("[3] Testing AUTH_REQUIRED on Cloud tools when unauthenticated...")
                res_unauth = await session.call_tool("project_context", {"project_id": "proj-test"})
                assert res_unauth.isError is True, "Expected isError=True for unauthenticated tool call"
                assert len(res_unauth.content) > 0
                err_text = res_unauth.content[0].text
                assert "AUTH_REQUIRED" in err_text or "AUTHORITY_UNAVAILABLE" in err_text
                print(f"    ✓ Safe error returned: {err_text}")

                # 5. Invalid / Unknown tool call (Section 2)
                res_unknown = await session.call_tool("unknown_tool_xyz", {})
                assert res_unknown.isError is True
                assert "unknown tool" in res_unknown.content[0].text
                print(f"    ✓ Unknown tool handled safely: {res_unknown.content[0].text}")

                # 6. List resources
                res_list = await session.list_resources()
                print(f"[4] Resource Discovery: {len(res_list.resources)} resources found")
                assert len(res_list.resources) >= 1
                assert str(res_list.resources[0].uri) == "opsi://project/context"

    print("=== Real MCP Client Protocol Acceptance: ALL PASS ===")

if __name__ == "__main__":
    asyncio.run(run_mcp_client_suite())
