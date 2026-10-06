#!/usr/bin/env python3
"""Update only the private detector, pinning one upstream commit per release."""
import argparse
import copy
import datetime
import gzip
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request
import uuid

REPOSITORY = "https://github.com/Ikaleio/lm-detector"
API = "https://api.github.com/repos/Ikaleio/lm-detector"
RAW = "https://raw.githubusercontent.com/Ikaleio/lm-detector"
DATA = {"data/shared_detector.json", "data/unified_bank.json", "data/unified_reference.jsonl"}
WRAPPER = ("Dockerfile", "package.json", "detector.ts", "server.ts", "detector.test.ts")
HEX40 = re.compile(r"[a-f0-9]{40}\Z")
HEX64 = re.compile(r"[a-f0-9]{64}\Z")


def run(args, capture=False, **kwargs):
    result = subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE if capture else None, **kwargs)
    return result.stdout.strip() if capture else None


def download(url, limit=64 * 1024 * 1024):
    req = urllib.request.Request(url, headers={"User-Agent": "sub2api-lm-detector-updater", "Accept": "application/vnd.github+json" if url.startswith(API) else "*/*"})
    with urllib.request.urlopen(req, timeout=60) as response:
        data = response.read(limit + 1)
    if len(data) > limit:
        raise ValueError("Upstream artifact exceeds download limit")
    return data


def resolve_revision(ref):
    info = json.loads(download(API + "/commits/" + urllib.parse.quote(ref, safe=""), 2 * 1024 * 1024))
    revision = info.get("sha", "")
    if not HEX40.fullmatch(revision):
        raise ValueError("GitHub did not return a full commit ID")
    return revision


def source_paths(tree):
    if tree.get("truncated"):
        raise ValueError("Incomplete upstream file listing")
    files = []
    for entry in tree["tree"]:
        path = entry["path"]
        if path in DATA or path == "LICENSE" or path.startswith("shared/"):
            if entry["type"] == "tree":
                continue
            parts = PurePosixPath(path).parts
            if ".." in parts or "\\" in path or path.startswith("/") or entry.get("mode") not in ("100644", "100755"):
                raise ValueError("Unsafe upstream file: " + path)
            files.append(path)
    if not (DATA | {"LICENSE", "shared/shared-detector.ts", "shared/challenge-browser.js"}).issubset(files):
        raise ValueError("Official shared modules or bundled data are missing")
    if len(files) > 500:
        raise ValueError("Unexpected upstream module count")
    return sorted(files)


def vendor_release(template, target, revision):
    target.mkdir(parents=True)
    for name in WRAPPER:
        shutil.copyfile(template / name, target / name)
    tree = json.loads(download(API + "/git/trees/" + revision + "?recursive=1", 8 * 1024 * 1024))
    manifest = {"repository": REPOSITORY, "revision": revision, "files": {}}
    total = 0
    for path in source_paths(tree):
        data = download(RAW + "/" + revision + "/" + urllib.parse.quote(path, safe="/"))
        total += len(data)
        if total > 128 * 1024 * 1024 or data.startswith(b"version https://git-lfs.github.com/spec/"):
            raise ValueError("Unsupported or oversized upstream artifacts")
        # These hashes are the service's required startup integrity manifest.
        manifest["files"][path] = hashlib.sha256(data).hexdigest()
        output = target / "vendor" / (path + ".gz" if path in DATA else path)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_bytes(gzip.compress(data, mtime=0) if path in DATA else data)
    (target / "upstream.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


def validate_compose(config):
    if set(config.get("services", {})) != {"lm-detector"}:
        raise ValueError("Refusing a Compose file containing services other than lm-detector")
    service = config["services"]["lm-detector"]
    networks = service.get("networks", {})
    if service.get("ports") or len(networks) != 1:
        raise ValueError("Detector must have one private network and no published ports")
    network = config.get("networks", {}).get(next(iter(networks)), {})
    if network.get("external") is not True or not network.get("name"):
        raise ValueError("Expected the existing external Sub2API network")
    if not service.get("image") or not service.get("build", {}).get("context"):
        raise ValueError("Detector image/build context is missing")
    return network["name"]


def validate_info(info, revision):
    if info.get("provider") != "lm_fingerprint_detector" or info.get("protocol") != 1 or info.get("algorithm") != "shared-detector-v1":
        raise ValueError("Detector protocol is incompatible with Sub2API; adapter update required")
    if info.get("revision") != revision or not HEX40.fullmatch(revision):
        raise ValueError("Unexpected running detector revision")
    for key in ("ranker_sha256", "reference_sha256", "calibration_sha256"):
        if not HEX64.fullmatch(info.get(key, "")):
            raise ValueError("Missing valid detector identity: " + key)
    if not info.get("bank_built_at"):
        raise ValueError("Missing reference bank version")
    return ":".join(info[key] for key in ("revision", "ranker_sha256", "reference_sha256", "calibration_sha256"))


def atomic_write(path, content):
    fd, temp = tempfile.mkstemp(prefix="." + path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def serialize(config):
    # JSON is valid Compose YAML; retain all existing settings, using resolved paths.
    return json.dumps(config, indent=2) + "\n"


class Deployment:
    def __init__(self, root, sub2api):
        self.root = root
        self.compose = root / "compose.yml"
        self.sub2api = sub2api

    def compose_command(self, *args):
        return ["docker", "compose", "-p", "lm-detector", "-f", str(self.compose), *args]

    def configuration(self):
        config = json.loads(run(self.compose_command("config", "--format", "json"), capture=True))
        validate_compose(config)
        return config

    def query(self, host, path, version=None):
        args = ["docker", "exec", self.sub2api, "wget", "-T", "15", "-q", "-O", "-"]
        if version:
            args += ["--header", "X-LM-Detector-Version: " + version]
        return json.loads(run([*args, "http://" + host + ":8080" + path], capture=True, timeout=25))

    def health(self, host, revision):
        if self.query(host, "/healthz").get("ready") is not True:
            raise ValueError("Detector is not ready")
        info = self.query(host, "/api/info")
        version = validate_info(info, revision)
        models = self.query(host, "/api/banks", version)["shared"]["models"]
        ids = [model["id"] for model in models]
        if not ids or len(ids) > 1000 or len(ids) != len(set(ids)) or any(not isinstance(i, str) or not i for i in ids):
            raise ValueError("Invalid detector candidate model list")
        if len(self.query(host, "/api/challenges", version)["challenges"]) != 3:
            raise ValueError("Detector must provide three challenges")
        return info, len(ids)

    def preflight(self, image, network, revision):
        name = "lm-detector-check-" + uuid.uuid4().hex[:12]
        try:
            run(["docker", "run", "-d", "--name", name, "--network", network, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", image], capture=True)
            deadline = time.monotonic() + 120
            while time.monotonic() < deadline:
                state = json.loads(run(["docker", "inspect", "--format", "{{json .State}}", name], capture=True))
                if not state["Running"] or state.get("Health", {}).get("Status") == "unhealthy":
                    raise RuntimeError("Candidate container failed startup validation")
                if state.get("Health", {}).get("Status") == "healthy":
                    return self.health(name, revision)
                time.sleep(2)
            raise TimeoutError("Candidate health check timed out")
        finally:
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def up(self):
        run(self.compose_command("up", "-d", "--no-deps", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "120", "lm-detector"))

    def switch(self, config, revision, previous, previous_revision):
        before = self.compose.read_text(encoding="utf-8")
        backup = self.root / "backups" / (datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:6] + ".json")
        backup.parent.mkdir(exist_ok=True)
        atomic_write(backup, serialize(previous))
        try:
            atomic_write(self.compose, serialize(config))
            self.up()
            self.health("lm-detector", revision)
            atomic_write(self.root / "rollback.json", serialize(previous))
        except BaseException:
            print("Switch failed; restoring the previous detector...", flush=True)
            atomic_write(self.compose, before)
            try:
                self.up()
                self.health("lm-detector", previous_revision)
            except BaseException as rollback_error:
                print("ROLLBACK NEEDS ATTENTION: " + str(rollback_error), file=sys.stderr)
                print("Previous Compose backup: " + str(backup), file=sys.stderr)
            raise


def main():
    parser = argparse.ArgumentParser(description="Update only lm-detector from one official upstream commit; no Sub2API/database changes.")
    parser.add_argument("--ref", default="main", help="official commit/tag/branch; main by default, resolved to a fixed commit")
    parser.add_argument("--check", action="store_true", help="download, build, test and check connectivity, without replacing the running service")
    parser.add_argument("--rollback", action="store_true", help="restore the previous successful deployment")
    parser.add_argument("--directory", type=Path, default=Path(__file__).resolve().parent, help="standalone deployment directory containing compose.yml")
    parser.add_argument("--sub2api-container", default="sub2api", help="existing Sub2API container used for private-network checks")
    args = parser.parse_args()
    if args.rollback and (args.check or args.ref != "main"):
        parser.error("--rollback cannot be combined with --check or --ref")
    import fcntl  # Server utility: Linux only. No extra Python dependencies.
    os.umask(0o022)
    root = args.directory.resolve(strict=True)
    with (root / ".update.lock").open("w") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("Another detector update is running") from None
        deploy = Deployment(root, args.sub2api_container)
        previous = deploy.configuration()
        network = validate_compose(previous)
        template = Path(previous["services"]["lm-detector"]["build"]["context"])
        old_revision = json.loads((template / "upstream.json").read_text())["revision"]
        # Recovery must also work when the currently deployed container is unhealthy.
        if not args.rollback:
            deploy.health("lm-detector", old_revision)
        if args.rollback:
            candidate = json.loads((root / "rollback.json").read_text())
            rollback_network = validate_compose(candidate)
            source = Path(candidate["services"]["lm-detector"]["build"]["context"])
            revision = json.loads((source / "upstream.json").read_text())["revision"]
            deploy.preflight(candidate["services"]["lm-detector"]["image"], rollback_network, revision)
            deploy.switch(candidate, revision, previous, old_revision)
        else:
            revision = resolve_revision(args.ref)
            print("Running: " + old_revision + "\nTarget:  " + revision, flush=True)
            if revision == old_revision and not args.check:
                print("Already at this revision. Nothing restarted. Use --check to revalidate.")
                return
            if shutil.disk_usage(root).free < 2 * 1024**3:
                raise RuntimeError("At least 2 GiB free disk space is required to build without removing the old release")
            stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
            release_id = revision[:12] + "-" + stamp + "-" + uuid.uuid4().hex[:6]
            release = root / "releases" / release_id / "services" / "lm-detector"
            print("Downloading official shared code and data...", flush=True)
            vendor_release(template, release, revision)
            image = "sub2api-lm-detector:" + release_id.lower()
            run(["docker", "build", "-t", image, str(release)])
            # Test the exact candidate wrapper/vendor files offline. No real model requests.
            run(["docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m", "--mount", "type=bind,src=" + str(release) + ",dst=/app,readonly", image, "bun", "test", "detector.test.ts"])
            info, count = deploy.preflight(image, network, revision)
            atomic_write(release / "validated-info.json", json.dumps(info, indent=2) + "\n")
            print("Validation passed; candidate models: " + str(count), flush=True)
            if args.check:
                print("CHECK ONLY: current container and Compose file are unchanged. Candidate retained at " + str(release))
                return
            candidate = copy.deepcopy(previous)
            candidate["services"]["lm-detector"]["build"]["context"] = str(release)
            candidate["services"]["lm-detector"]["image"] = image
            deploy.switch(candidate, revision, previous, old_revision)
        print("Detector ready at revision " + revision)
        print("URL unchanged: http://lm-detector:8080")
        print("In Sub2API: check the connection, verify expected models, and SAVE the newly accepted detector version before enabling automatic tests.")
        print("Sub2API, PostgreSQL, Redis and administrator configuration were not modified.")


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print("Detector update failed: " + (str(error) or "interrupted"), file=sys.stderr)
        sys.exit(1)
