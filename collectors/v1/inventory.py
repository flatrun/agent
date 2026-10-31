import hashlib
import argparse
import email.parser
import json
import os
import re
import stat
import subprocess
import zipfile
from pathlib import Path, PurePosixPath


def collect(root, runtime, preferred=None):
    preferred = preferred or set()
    items, omissions, boundaries = [], [], []
    suffixes = {".php", ".phtml", ".phar", ".php3", ".php4", ".php5", ".php7", ".php8", ".cgi", ".pl", ".py", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".go"}
    content_suffixes = suffixes | {".html", ".htm", ".svg"}
    count = 0
    for directory, dirs, files in os.walk(root, followlinks=False, onerror=lambda e: omissions.append({"path": ".", "reason": "directory unreadable"})):
        prefix = Path(directory).relative_to(root)
        dirs.sort(key=lambda name: (not any(path.startswith(str(prefix / name).removeprefix("./") + "/") for path in preferred), name))
        files.sort(key=lambda name: (str(prefix / name).removeprefix("./") not in preferred, name))
        for name in list(dirs):
            candidate = Path(directory) / name
            if candidate.is_symlink():
                boundaries.append({"path": str(candidate.relative_to(root)), "reason": "symbolic link"})
                dirs.remove(name)
        for name in files:
            count += 1
            if count > 10000:
                omissions.append({"path": ".", "reason": "file limit"})
                return items, omissions, boundaries
            file = Path(directory) / name
            relative = str(file.relative_to(root))
            try:
                info = file.lstat()
                if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                    boundaries.append({"path": relative, "reason": "link or unsupported format"})
                    continue
                if info.st_size > (64 * 1024 * 1024 if runtime == "go" else 1024 * 1024):
                    omissions.append({"path": relative, "reason": "oversized file"})
                    continue
                indicators = []
                if name in (".htaccess", ".user.ini", "web.config"):
                    indicators.append("handler_or_configuration_override")
                if file.suffix.lower() in suffixes:
                    indicators.append("potential_code_requires_loader_context")
                if file.suffix.lower() in content_suffixes:
                    text = file.read_text(errors="replace")
                    if re.search(r"<\?(?:php\b|=|(?!(?:xml)\b)\s)", text, re.I):
                        indicators.append("php_tag")
                    if re.search(r"<(?:script|foreignObject)\b|\bon\w+\s*=|javascript:", text, re.I):
                        indicators.append("active_content")
                    if re.search(r"\b(?:eval|exec|system|shell_exec|unserialize|pickle\.loads|subprocess\.\w+)\s*\(", text):
                        indicators.append("source_call_requires_context")
                if indicators:
                    items.append({"path": relative, "sha256": hashlib.sha256(file.read_bytes()).hexdigest(), "indicators": indicators})
                if zipfile.is_zipfile(file):
                    omissions.append({"path": relative, "reason": "archive contents not inspected; metadata only"})
                    with zipfile.ZipFile(file) as archive:
                        expanded = 0
                        for index, member in enumerate(archive.infolist()):
                            if index >= 1000:
                                omissions.append({"path": relative, "reason": "archive member limit"})
                                break
                            risks = []
                            parts = PurePosixPath(member.filename.replace("\\", "/"))
                            if parts.is_absolute() or ".." in parts.parts or re.match(r"^[A-Za-z]:", member.filename):
                                risks.append("archive_path_escape")
                            if stat.S_ISLNK(member.external_attr >> 16):
                                risks.append("archive_link")
                            expanded += member.file_size
                            if expanded > 64 * 1024 * 1024 or member.file_size > max(1, member.compress_size) * 100:
                                risks.append("archive_expansion_limit")
                            if member.flag_bits & 1:
                                omissions.append({"path": relative, "reason": "encrypted archive member"})
                            if member.filename.lower().endswith((".zip", ".tar", ".gz", ".7z", ".rar")):
                                omissions.append({"path": relative, "reason": "nested archive not inspected"})
                            if member.filename.lower().endswith("vbaproject.bin"):
                                risks.append("document_macro")
                            if risks:
                                items.append({"path": relative, "sha256": hashlib.sha256(file.read_bytes()).hexdigest(), "indicators": risks})
                elif file.suffix.lower() in (".zip", ".tar", ".gz", ".7z", ".rar", ".pdf", ".doc", ".xls"):
                    omissions.append({"path": relative, "reason": "unsupported or malformed attachment format"})
                if runtime == "php" and relative.endswith("vendor/composer/installed.json"):
                    data = json.loads(file.read_text())
                    packages = data if isinstance(data, list) else data.get("packages", [])
                    for package in packages:
                        items.append({"ecosystem": "composer", "name": package["name"], "version": package["version"], "identity": "installed_metadata"})
                if runtime == "php" and name == "composer.lock":
                    data = json.loads(file.read_text())
                    for group in ("packages", "packages-dev"):
                        for package in data.get(group, []):
                            items.append({"ecosystem": "composer", "name": package["name"], "version": package["version"], "identity": "declared", "group": group})
                if runtime == "javascript" and name == "package.json":
                    data = json.loads(file.read_text())
                    if data.get("name") and data.get("version"):
                        items.append({"ecosystem": "npm", "name": data["name"], "version": data["version"], "identity": "installed_metadata" if "node_modules" in file.parts else "declared"})
                if runtime == "javascript" and name == "package-lock.json":
                    data = json.loads(file.read_text())
                    if data.get("lockfileVersion") not in (2, 3):
                        omissions.append({"path": relative, "reason": "unsupported npm lock format"})
                    else:
                        for location, package in data.get("packages", {}).items():
                            if not location or not package.get("version"):
                                continue
                            identity = "declared" if package.get("name") else "path_candidate"
                            package_name = package.get("name") or location.rsplit("node_modules/", 1)[-1]
                            items.append({"ecosystem": "npm", "name": package_name, "version": package["version"], "identity": identity})
                if runtime == "python" and name == "METADATA" and file.parent.name.endswith(".dist-info"):
                    metadata = email.parser.Parser().parsestr(file.read_text())
                    if metadata.get("Name") and metadata.get("Version"):
                        items.append({"ecosystem": "pypi", "name": metadata["Name"], "version": metadata["Version"], "identity": "installed_metadata"})
                with file.open("rb") as stream:
                    binary_magic = stream.read(4)
                if runtime == "go" and binary_magic == b"\x7fELF":
                    env = {"PATH": "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "GOTOOLCHAIN": "local", "GOWORK": "off", "GOENV": "off", "GOFLAGS": "", "GOPROXY": "off", "GOSUMDB": "off", "HOME": "/tmp"}
                    process = subprocess.run(["go", "version", "-m", "-json", str(file)], cwd="/work", env=env, capture_output=True, timeout=5, check=True)
                    build = json.loads(process.stdout)
                    items.append({"ecosystem": "go", "version": build.get("GoVersion"), "identity": "binary_metadata"})
                    for module in [build.get("Main", {})] + build.get("Deps", []):
                        replacement = module.get("Replace", module)
                        if replacement.get("Path") and replacement.get("Version"):
                            items.append({"ecosystem": "go", "name": replacement["Path"], "version": replacement["Version"], "identity": "binary_metadata"})
            except (OSError, ValueError, KeyError, TypeError, MemoryError, RecursionError, zipfile.BadZipFile, subprocess.SubprocessError):
                omissions.append({"path": relative, "reason": "unreadable or unsupported metadata"})
    return items, omissions, boundaries


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", choices=("php", "javascript", "python", "go", "unknown"), required=True)
    args = parser.parse_args()
    root = Path("/evidence")
    if not root.is_dir():
        raise ValueError("Approved snapshot unavailable")
    manifest_status, changes = "unknown", []
    try:
        manifest_path = Path("/manifest/metadata.json")
        if manifest_path.stat().st_size <= 2 * 1024 * 1024:
            manifest = json.loads(manifest_path.read_text())
            if manifest.get("api_version") == "flatrun.security-manifest/v1":
                changes = manifest.get("changes", [])
                manifest_status = manifest.get("manifest_processing", "unknown")
    except (OSError, ValueError, TypeError):
        pass
    preferred = {change["path"] for change in changes if change.get("kind") in ("added", "changed")}
    items, omissions, boundaries = collect(root, args.runtime, preferred)
    indicator_count = sum(bool(item.get("indicators")) for item in items)
    items.append({"manifest_changes": changes})
    result = {
        "api_version": "flatrun.collector-result/v1", "process_status": "completed", "items": [{"indicator_count": indicator_count}] + items,
        "omissions": omissions, "boundaries": boundaries,
        "limitations": ["Source and attachment indicators require approved policy and runtime context", "Installed metadata must be bound to actual runtime roots", "Historical authorization, effective writer permissions and loader reachability are unknown", "Delivery headers and upload behavior have not been demonstrated"],
        "coverage": {"declared_observation": "partial" if omissions or boundaries else "complete", "manifest_processing": manifest_status, "window_coverage": "unknown"},
    }
    while len(json.dumps(result).encode()) > 60000 and len(result["items"]) > 1:
        result["items"].pop()
        result["coverage"]["declared_observation"] = "partial"
    if len(result["omissions"]) > 64:
        result["omissions"] = result["omissions"][:64] + [{"path": ".", "reason": "additional omissions exceed output budget"}]
    if len(result["boundaries"]) > 64:
        result["boundaries"] = result["boundaries"][:64] + [{"path": ".", "reason": "additional boundaries exceed output budget"}]
    if len(json.dumps(result).encode()) > 64000:
        result["omissions"] = [{"path": ".", "reason": "omission details exceed output budget"}]
        result["boundaries"] = []
        result["coverage"]["declared_observation"] = "partial"
    print(json.dumps(result))


if __name__ == "__main__":
    main()
