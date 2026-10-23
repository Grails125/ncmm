#!/usr/bin/env python3
"""固定发布版本，校验并按需原子预留指向待发布源码的版本标签。"""

import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path


def git(*args):
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True, timeout=60).stdout.strip()


def remote_tag_commit(version):
    tag = "refs/tags/v" + version
    # 不使用本地标签缓存；annotated tag 必须比较 peel 后的真实提交。
    records = {}
    for line in git("ls-remote", "--tags", "origin", tag, tag + "^{}").splitlines():
        sha, name = line.split("\t", 1)
        records[name] = sha
    return records.get(tag + "^{}", records.get(tag))


def validate_release(source_sha, version, ref):
    if not re.fullmatch(r"[0-9a-f]{40}", source_sha) or git("rev-parse", "HEAD^{commit}") != source_sha:
        raise ValueError("待发布源码与已固定的提交不一致")
    if ref.startswith("refs/tags/"):
        version = ref.removeprefix("refs/tags/")
    elif version in ("", "auto"):
        version = Path("VERSION").read_text(encoding="utf-8").strip()
    if not re.fullmatch(r"v?[0-9]+\.[0-9]+\.[0-9]+-grails(?:\.[0-9]+)?", version):
        raise ValueError("发布版本必须是本仓库原生版本，例如 1.2.3-grails")
    version = version.removeprefix("v")
    existing = remote_tag_commit(version)
    if existing is not None and existing != source_sha:
        raise ValueError("该版本标签已存在且指向其他源码，请使用新的原生版本号")
    return {"version": version, "commitSha": source_sha, "tagExists": existing is not None}


def reserve_release_tag(result):
    if result["tagExists"]:
        return result
    repository = os.environ.get("GITHUB_REPOSITORY", "")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("创建版本标签需要有效的发布仓库")
    if not (os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")):
        raise ValueError("创建版本标签需要发布令牌")
    # 只使用创建 ref 的 POST，不执行更新或强制推送；认证仅通过环境传递。
    try:
        subprocess.run(["gh", "api", "--hostname", "github.com", "--method", "POST",
                        f"repos/{repository}/git/refs", "--raw-field", "ref=refs/tags/v" + result["version"],
                        "--raw-field", "sha=" + result["commitSha"]],
                       check=False, capture_output=True, text=True, timeout=60)
    except (OSError, subprocess.TimeoutExpired):
        # 响应丢失或另一个产物抢先创建时，只能依靠远端实际状态确认结果。
        pass
    actual = remote_tag_commit(result["version"])
    if actual is None:
        raise ValueError("创建远端版本标签失败，请检查发布权限和网络后重试")
    if actual != result["commitSha"]:
        raise ValueError("该版本标签已存在且指向其他源码，请使用新的原生版本号")
    return {**result, "tagExists": True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-sha", required=True)
    parser.add_argument("--version", default="auto")
    parser.add_argument("--ref", default="")
    parser.add_argument("--output")
    parser.add_argument("--reserve-tag", action="store_true", help="发布前原子创建或确认远端版本标签")
    args = parser.parse_args()
    try:
        result = validate_release(args.source_sha, args.version, args.ref)
        if args.reserve_tag:
            result = reserve_release_tag(result)
        if args.output:
            with open(args.output, "a", encoding="utf-8") as output:
                output.write(f"version={result['version']}\ntag_exists={str(result['tagExists']).lower()}\n")
        print(json.dumps(result))
    except (OSError, ValueError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        print(f"发布校验失败：{error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
