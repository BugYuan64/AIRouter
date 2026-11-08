"""Opt-in bounded inference verification. Secrets stay in memory, never in reports."""
import argparse
import datetime
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import time

if hasattr(sys.stdout, "reconfigure"):
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stderr.reconfigure(encoding="utf-8")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ssh-target", required=True)
    parser.add_argument("--identity", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--cpa-config", default="/path/to/cliproxyapi/data/config.yaml")
    parser.add_argument("--report", default="live-check.json")
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    hidden = {"creationflags": subprocess.CREATE_NO_WINDOW} if os.name == "nt" else {}
    ssh = ["ssh", "-i", str(Path(args.identity).resolve()), "-o", "BatchMode=yes",
           "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
           "-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "ConnectTimeout=12"]
    # stdout from this subprocess is captured privately, never forwarded to tool output.
    source = ("import pathlib,yaml,sys; c=yaml.safe_load(pathlib.Path("
              + repr(args.cpa_config) + ").read_text()); "
              "keys=c.get('access',{}).get('api-keys',[]); "
              "k=next((v for v in keys if isinstance(v,str) and v),''); "
              "sys.stdout.write(k); sys.exit(0 if k else 3)")
    key = ""
    tunnel = None
    try:
        result = subprocess.run(ssh + [args.ssh_target, "python3 -c " + shlex.quote(source)],
                                capture_output=True, text=True, timeout=25, **hidden)
        if result.returncode != 0 or not result.stdout.strip():
            print("Cannot read the existing CPA data-plane credential securely.", file=sys.stderr)
            return 2
        key = result.stdout.strip()
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        tunnel = subprocess.Popen(ssh + ["-N", "-o", "ExitOnForwardFailure=yes", "-L",
                                        f"127.0.0.1:{port}:127.0.0.1:8317", args.ssh_target],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, **hidden)
        for _ in range(100):
            if tunnel.poll() is not None:
                raise RuntimeError("SSH tunnel failed")
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                    break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("SSH tunnel did not become ready")
        env = os.environ.copy()
        env.update(AIROUTER_LIVE_CPA_URL=f"http://127.0.0.1:{port}",
                   AIROUTER_LIVE_CPA_KEY=key, AIROUTER_LIVE_MODEL=args.model)
        print("Running 6 bounded inference checks (3 native protocols, JSON and SSE).", flush=True)
        test = subprocess.run(["go", "test", "./internal/httpapi", "-run", "^TestLiveCPA$",
                               "-v", "-count=1", "-timeout=15m"], cwd=root, env=env,
                              capture_output=True, text=True, timeout=930, **hidden)
        output = (test.stdout + test.stderr).replace(key, "[REDACTED]")
        records = []
        for line in output.splitlines():
            if "AIRouter_LIVE " in line:
                records.append(json.loads(line.split("AIRouter_LIVE ", 1)[1]))
        report = {
            "verified_at": datetime.datetime.now(datetime.timezone(datetime.timedelta(hours=8))).isoformat(),
            "scope": "local AIRouter HTTP handler -> strict SSH tunnel -> existing CPA -> real upstream",
            "upstream_model": args.model,
            "gateway_retries": 0,
            "cpa_internal_retries_changed": False,
            "cases": records,
            "passed": test.returncode == 0 and len(records) == 6 and all(r.get("passed") for r in records),
            "limitations": ["Only this model and these stateless requests were tested.",
                            "Final provider/account identity is not proven by the model field.",
                            "No VPS deployment, state migration, tool execution, or client UI acceptance."],
        }
        destination = (root / args.report).resolve()
        if not destination.is_relative_to(root):
            raise RuntimeError("Report must stay inside the project")
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(json.dumps({"passed": report["passed"], "cases": len(records),
                          "report": str(destination)}, ensure_ascii=False), flush=True)
        print(output, end="", flush=True)
        return 0 if report["passed"] else 1
    except (OSError, RuntimeError, subprocess.TimeoutExpired):
        print("Verification failed at the local setup or process boundary; no secrets printed.", file=sys.stderr)
        return 2
    finally:
        if tunnel is not None:
            tunnel.terminate()
            try:
                tunnel.communicate(timeout=10)
            except subprocess.TimeoutExpired:
                tunnel.kill()
                tunnel.communicate()
        key = ""


if __name__ == "__main__":
    raise SystemExit(main())
