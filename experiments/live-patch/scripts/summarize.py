#!/usr/bin/env python3
"""Turn Trivy JSON plus health and size files into a compact summary.

Buckets:
  os       Class os-pkgs (debian, alpine, ...)
  runtime  Go stdlib, or a package whose name is the language runtime
  app      bundler, npm, and Go modules other than stdlib
"""

import json
import sys
from pathlib import Path

SEVERITIES = ("CRITICAL", "HIGH", "MEDIUM", "LOW", "UNKNOWN")
RUNTIME_NAMES = {"stdlib", "ruby", "nodejs", "node"}


def empty():
    return {
        "findings": {s: 0 for s in SEVERITIES},
        "fixable": {s: 0 for s in SEVERITIES},
        "packages": [],
    }


def add(bucket, vuln):
    sev = vuln.get("Severity") or "UNKNOWN"
    if sev not in bucket["findings"]:
        sev = "UNKNOWN"
    bucket["findings"][sev] += 1
    fixed = (vuln.get("FixedVersion") or "").strip()
    if fixed:
        bucket["fixable"][sev] += 1
    bucket["packages"].append(
        {
            "id": vuln.get("VulnerabilityID"),
            "pkg": vuln.get("PkgName"),
            "installed": vuln.get("InstalledVersion"),
            "fixed": fixed,
            "severity": sev,
        }
    )


def bucket_for(result, vuln):
    klass = result.get("Class") or ""
    typ = result.get("Type") or ""
    name = vuln.get("PkgName") or ""
    if klass == "os-pkgs":
        return "os"
    if name in RUNTIME_NAMES or name.startswith("stdlib@"):
        return "runtime"
    if typ == "gobinary" and name == "stdlib":
        return "runtime"
    return "app"


def summarize(path):
    data = json.loads(Path(path).read_text())
    out = {"os": empty(), "runtime": empty(), "app": empty()}
    for result in data.get("Results") or []:
        for vuln in result.get("Vulnerabilities") or []:
            add(out[bucket_for(result, vuln)], vuln)
    # Package lists make the file large. Keep a few examples of fixable HIGH/CRITICAL.
    for key, bucket in out.items():
        examples = [
            p
            for p in bucket["packages"]
            if p["severity"] in ("HIGH", "CRITICAL") and p["fixed"]
        ]
        # Unique pkg names, highest severity first.
        seen = []
        used = set()
        for p in sorted(examples, key=lambda p: (p["severity"] != "CRITICAL", p["pkg"] or "")):
            if p["pkg"] in used:
                continue
            used.add(p["pkg"])
            seen.append({k: p[k] for k in ("pkg", "severity", "installed", "fixed")})
            if len(seen) == 8:
                break
        bucket["examples"] = seen
        del bucket["packages"]
    return out


def hc(bucket, fixable_only=False):
    src = bucket["fixable"] if fixable_only else bucket["findings"]
    return src["CRITICAL"] + src["HIGH"]


def main():
    raw = Path(sys.argv[1])
    summary = {}
    for path in sorted(raw.glob("*/*.trivy.json")):
        name, approach = path.parts[-2], path.name.replace(".trivy.json", "")
        item = summarize(path)
        size_path = path.with_name(approach + ".size")
        time_path = path.with_name(approach + ".time")
        health_path = path.with_name(approach + ".health")
        item["size"] = int(size_path.read_text().strip()) if size_path.exists() else None
        item["seconds"] = float(time_path.read_text().strip()) if time_path.exists() else None
        item["health"] = health_path.read_text() if health_path.exists() else ""
        summary.setdefault(name, {})[approach] = item
    dest = Path(sys.argv[2])
    dest.write_text(json.dumps(summary, indent=2) + "\n")
    for name, approaches in summary.items():
        for approach, item in approaches.items():
            print(
                f"{name:16} {approach:10} "
                f"os {hc(item['os']):4} ({hc(item['os'], True):4} fixable)  "
                f"runtime {hc(item['runtime']):4} ({hc(item['runtime'], True):4} fixable)  "
                f"app {hc(item['app']):4} ({hc(item['app'], True):4} fixable)  "
                f"size={item['size']} time={item['seconds']}"
            )


if __name__ == "__main__":
    main()
