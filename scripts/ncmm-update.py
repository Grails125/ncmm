#!/usr/bin/env python3
# -*- coding: utf-8 -*-

"""
cron: 1 1 1 1 1
new Env('NCMM 安装、更新')

更新规则（重点）：
  本脚本只允许更新带 -grails 标识（兼容旧 -grails.N）的本分支程序。版本比较、下载、解压和配置升级
  由该程序自身处理；首次安装仅从 Grails125/ncmm 获取发布包。

  * 已经装好 ncmm：进入 ncmm 程序目录执行 `ncmm update --apply`，
    脚本只负责"手动触发 + 日志回显 + 记录 VERSION"。
  * 还没有 ncmm：没有任何可调用的程序，只能由脚本下载 release 安装包完成**首次安装**
    （仅此一处保留脚本自身的下载逻辑，且只在没有二进制时才会走到）。
"""

import os
import sys
import platform
import urllib.request
import json
import re
import shutil
import zipfile
import tarfile
import subprocess
import stat
import tempfile

# 1. 稳定获取当前脚本所在的真实目录
current_dir = os.path.dirname(os.path.abspath(__file__))
target_dir = current_dir  # 直接使用脚本所在目录作为目标目录

# 手动触发更新时给 ncmm 的超时（秒）
NCMM_UPDATE_TIMEOUT = 600


def is_native_version(value, allow_tag=False):
    """统一校验本分支版本；仅发布标签允许 v 前缀，程序版本保持原样核对。"""
    if not isinstance(value, str):
        return False
    if allow_tag and value.startswith("v"):
        value = value[1:]
    return re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+-grails(?:\.[0-9]+)?', value) is not None


# 2. 识别系统平台与架构判定逻辑（仅首次安装时使用）
def get_platform_info():
    sys_name = platform.system().lower()
    arch_name = platform.machine().lower()
    
    if 'windows' in sys_name:
        os_part = "Windows"
        ext = ".zip"
    elif 'linux' in sys_name:
        os_part = "Linux"
        ext = ".tar.gz"
    elif 'darwin' in sys_name:
        os_part = "Darwin"
        ext = ".tar.gz"
    else:
        os_part = sys_name.capitalize()
        ext = ".tar.gz"
        
    if arch_name in ['x86_64', 'amd64']:
        arch_part = "x86_64"
    elif arch_name in ['arm64', 'aarch64']:
        arch_part = "arm64"
    elif 'arm' in arch_name:
        arch_part = "armv6"
    else:
        arch_part = arch_name
        
    return os_part, arch_part, ext

# 3. 获取 GitHub 最新 Release 标签与资产列表（仅首次安装时使用）
def get_latest_release():
    headers = {
        'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36'
    }
    
    # 1. 尝试 GitHub API
    api_urls = [
        "https://api.github.com/repos/Grails125/ncmm/releases/latest",
        "https://gh-proxy.com/https://api.github.com/repos/Grails125/ncmm/releases/latest"
    ]
    for url in api_urls:
        try:
            print(f"[LOG] 正在请求 GitHub API 获取最新版本 ({url})...")
            req = urllib.request.Request(url, headers=headers)
            with urllib.request.urlopen(req, timeout=10) as response:
                data = json.loads(response.read().decode('utf-8'))
                tag_name = data.get('tag_name')
                assets = data.get('assets', [])
                if tag_name:
                    return tag_name, assets
        except Exception as e:
            print(f"[WARNING] 请求 GitHub API 失败 ({url}): {e}")
            
    # 2. 备用方案：重定向解析 (依次尝试加速镜像与 GitHub 原地址兜底)
    redirect_urls = [
        "https://gh-proxy.com/https://github.com/Grails125/ncmm/releases/latest",
        "https://ghproxy.net/https://github.com/Grails125/ncmm/releases/latest",
        "https://githubproxy.cc/https://github.com/Grails125/ncmm/releases/latest",
        "https://github.com/Grails125/ncmm/releases/latest"
    ]
    for url in redirect_urls:
        try:
            print(f"[LOG] 正在通过网页重定向获取最新版本 ({url})...")
            req = urllib.request.Request(url, headers=headers)
            with urllib.request.urlopen(req, timeout=10) as response:
                final_url = response.geturl()
                match = re.search(r'/releases/tag/([^/]+)', final_url)
                if match:
                    tag_name = match.group(1)
                    return tag_name, None
        except Exception as e:
            print(f"[WARNING] 重定向方案获取失败 ({url}): {e}")
        
    return None, None

# 4. 带有多镜像重试与原地址兜底的下载模块（仅首次安装时使用）
PROXIES = [
    "https://gh-proxy.com/",
    "https://ghproxy.net/",
    "https://githubproxy.cc/",
    ""  # 原地址直连兜底
]

def download_file_with_fallback(src_url, dst_path, headers=None, timeout=45):
    """
    依次尝试加速镜像与 GitHub 原始地址兜底下载文件
    """
    if not headers:
        headers = {
            'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36'
        }

    clean_url = src_url
    for p in PROXIES:
        if p and clean_url.startswith(p):
            clean_url = clean_url[len(p):]
            break

    candidate_urls = []
    is_github = any(clean_url.startswith(prefix) for prefix in [
        "https://github.com/",
        "https://raw.githubusercontent.com/",
        "https://objects.githubusercontent.com/"
    ])

    if is_github:
        for prefix in PROXIES:
            candidate_urls.append(f"{prefix}{clean_url}")
    else:
        candidate_urls.append(clean_url)

    for idx, url in enumerate(candidate_urls, start=1):
        is_direct = url == clean_url
        desc = "GitHub 原地址直连" if is_direct else "加速镜像"
        print(f"[LOG] 下载尝试 [{idx}/{len(candidate_urls)}] ({desc}): {url}")
        try:
            req = urllib.request.Request(url, headers=headers)
            with urllib.request.urlopen(req, timeout=timeout) as response, open(dst_path, 'wb') as out_file:
                shutil.copyfileobj(response, out_file)
            print(f"[LOG] 成功下载文件: {url}")
            return True
        except Exception as e:
            print(f"[WARNING] 从 {url} 下载失败: {e}，尝试下一个备用源...")

    return False

# 5. 本地 VERSION 文件读写（仅作记录，供面板展示；是否更新完全由 ncmm 判断）
def read_local_version(version_file):
    if not os.path.exists(version_file):
        return "unknown"
    try:
        with open(version_file, 'r', encoding='utf-8') as f:
            return f.read().strip() or "unknown"
    except Exception as e:
        print(f"[WARNING] 无法读取本地 VERSION 文件: {e}")
        return "unknown"

def write_local_version(version_file, version):
    try:
        with open(version_file, 'w', encoding='utf-8') as f:
            f.write(version.strip().lstrip('vV') + '\n')
        print(f"[LOG] 已更新本地 VERSION 文件为: {version.strip().lstrip('vV')}")
        return True
    except Exception as e:
        print(f"[WARNING] 写入 VERSION 文件失败: {e}")
        return False

# 6. 调用 ncmm 自身完成更新（手动触发的核心）
def run_ncmm_cli(binary_path, args, timeout=NCMM_UPDATE_TIMEOUT, capture=False):
    """在 ncmm 程序目录内执行 ncmm 子命令，返回 (returncode, stdout+stderr)。"""
    print(f"[LOG] 进入程序目录执行: {os.path.basename(binary_path)} {' '.join(args)}")
    try:
        proc = subprocess.run(
            [binary_path] + args,
            cwd=target_dir,
            timeout=timeout,
            stdout=subprocess.PIPE if capture else None,
            stderr=subprocess.STDOUT if capture else None,
            text=True,
        )
        output = (proc.stdout or "") if capture else ""
        return proc.returncode, output
    except subprocess.TimeoutExpired:
        print(f"[ERROR] 执行 ncmm {' '.join(args)} 超时（>{timeout}s）")
        return 124, ""
    except Exception as e:
        print(f"[ERROR] 执行 ncmm {' '.join(args)} 失败: {e}")
        return 1, ""

def query_binary_version(binary_path):
    """读取二进制自身报告的版本号，用于更新来源检查和 VERSION 记录。"""
    code, output = run_ncmm_cli(binary_path, ["--version"], timeout=30, capture=True)
    if code != 0:
        return None
    match = re.search(r'Version:\s*(\S+)', output)
    if match:
        return match.group(1)
    return None

def update_with_ncmm(binary_path, binary_name):
    """
    手动触发 ncmm 更新：只调用 `ncmm update --apply`。
    检查、下载、镜像、解压、替换、配置升级全部由 ncmm 按自身规则完成。
    返回 CLI 的退出码；失败由用户按原始错误排查，不终止进程或自动重试。
    """
    code, _ = run_ncmm_cli(binary_path, ["update", "--apply"])
    if code == 0:
        return 0

    print(f"[ERROR] ncmm update --apply 执行失败（状态码 {code}），本次更新未完成。")
    print(f"[LOG] 请根据上方错误检查网络、文件权限和配置，再手动重试；脚本不会终止其他 {binary_name} 进程。")
    return code

# 7. 首次安装（没有任何可调用的 ncmm 二进制时）：先验真，再落地
MAX_INSTALL_FILE_BYTES = 256 * 1024 * 1024


def extract_install_files(archive_path, ext, binary_name, directory):
    """只读取普通程序和配置文件，不向磁盘展开包内路径或链接。"""
    package = zipfile.ZipFile(archive_path) if ext == ".zip" else tarfile.open(archive_path, "r:gz")
    result = {}
    with package:
        members = package.infolist() if ext == ".zip" else package.getmembers()
        for member in members:
            name = member.filename if ext == ".zip" else member.name
            base = name.replace("\\", "/").rsplit("/", 1)[-1]
            if base not in (binary_name, "config.yaml"):
                continue
            if ext == ".zip":
                kind = stat.S_IFMT(member.external_attr >> 16)
                regular = not member.is_dir() and kind in (0, stat.S_IFREG)
                size = member.file_size
            else:
                regular, size = member.isfile(), member.size
            if not regular or base in result or size > MAX_INSTALL_FILE_BYTES:
                raise ValueError("安装包文件不是唯一、大小受限的普通文件")
            destination = os.path.join(directory, base)
            source = package.open(member) if ext == ".zip" else package.extractfile(member)
            with source, open(destination, "xb") as output:
                remaining = MAX_INSTALL_FILE_BYTES
                while True:
                    chunk = source.read(min(1024 * 1024, remaining + 1))
                    if not chunk:
                        break
                    remaining -= len(chunk)
                    if remaining < 0:
                        raise ValueError("安装包文件超出大小限制")
                    output.write(chunk)
            result[base] = destination
    return result


def install_from_archive(remote_tag, assets, binary_name, binary_path, config_path, version_file):
    if not is_native_version(remote_tag, allow_tag=True):
        print("[ERROR] 首次安装仅接受本仓库的原生评分版本标签。")
        return False
    if os.path.lexists(binary_path) or os.path.lexists(binary_path + ".old"):
        print("[ERROR] 程序或恢复备份已存在，已停止首次安装并保留现有文件。")
        return False

    os_part, arch_part, ext = get_platform_info()
    asset_filename = f"ncmm_{os_part}_{arch_part}{ext}"
    download_url = f"https://github.com/Grails125/ncmm/releases/download/{remote_tag}/{asset_filename}"
    for asset in assets or []:
        if asset.get("name") == asset_filename and asset.get("browser_download_url") != download_url:
            print("[ERROR] 发布资源来源与本仓库、目标标签或平台文件不一致，安装中止。")
            return False

    installed = []
    committed = False
    try:
        # 与目标目录同一文件系统，确保最终硬链接创建是原子的且不能覆盖既有文件。
        with tempfile.TemporaryDirectory(prefix=".ncmm-install-", dir=target_dir) as directory:
            archive_path = os.path.join(directory, asset_filename)
            if not download_file_with_fallback(download_url, archive_path, timeout=45):
                print("[ERROR] 无法从本仓库下载目标安装包，安装中止。")
                return False
            files = extract_install_files(archive_path, ext, binary_name, directory)
            candidate = files.get(binary_name)
            if not candidate:
                raise ValueError("安装包中缺少目标程序")
            if 'windows' not in platform.system().lower():
                os.chmod(candidate, 0o755)
            expected_version = remote_tag[1:] if remote_tag.startswith("v") else remote_tag
            if query_binary_version(candidate) != expected_version:
                raise ValueError("安装包程序实际版本与目标原生版本不一致")

            default_config = files.get("config.yaml")
            if not os.path.lexists(config_path) and not default_config:
                default_config = os.path.join(directory, "config.yaml")
                config_url = f"https://raw.githubusercontent.com/Grails125/ncmm/{remote_tag}/config/config.yaml"
                if not download_file_with_fallback(config_url, default_config, timeout=15):
                    raise ValueError("无法获取目标版本的默认配置")

            version_candidate = os.path.join(directory, "VERSION")
            with open(version_candidate, "w", encoding="utf-8") as output:
                output.write(expected_version + "\n")
            candidate_stat = os.stat(candidate)
            os.link(candidate, binary_path)
            installed.append((binary_path, candidate_stat))
            if not os.path.lexists(config_path):
                try:
                    config_stat = os.stat(default_config)
                    os.link(default_config, config_path)
                    installed.append((config_path, config_stat))
                except FileExistsError:
                    # 另一个进程先写入的用户配置也必须保留。
                    pass
            os.replace(version_candidate, version_file)
            committed = True
            print(f"[SUCCESS] ncmm 已验证并安装 {remote_tag}，已有配置保持不变。")
            return True
    except Exception as error:
        if committed:
            print(f"[WARNING] 安装已经完成，但临时目录清理失败: {error}")
            return True
        print(f"[ERROR] 首次安装失败，已保留原有文件: {error}")
        # 只回收本次创建的文件，不删除其他进程替换后的文件。
        for destination, created in reversed(installed):
            try:
                current = os.lstat(destination)
                if (current.st_dev, current.st_ino) == (created.st_dev, created.st_ino):
                    os.remove(destination)
            except OSError as cleanup_error:
                print(f"[WARNING] 清理本次安装文件失败: {cleanup_error}")
        return False

def main():
    print(f"[LOG] Python脚本所在目录: {current_dir}")
    print(f"[LOG] 目标二进制目录: {target_dir}")
    is_windows = 'windows' in platform.system().lower()
    binary_name = "ncmm.exe" if is_windows else "ncmm"
    binary_path = os.path.join(target_dir, binary_name)
    config_path = os.path.join(target_dir, "config.yaml")
    version_file = os.path.join(target_dir, "VERSION")

    # 没有可调用的二进制：只能由脚本完成首次安装（此时不存在"两套更新规则"的问题）
    if not os.path.exists(binary_path):
        old_path = binary_path + ".old"
        if os.path.lexists(old_path):
            print(f"[ERROR] 主程序缺失，但发现更新恢复备份: {old_path}，已停止首次安装。")
            print(f"[LOG] 请先确认该备份为完整的本分支可执行文件，再手动恢复到 {binary_path} 后重试；脚本保留现有文件不作覆盖。")
            sys.exit(1)
        print(f"[LOG] 未检测到 {binary_name}，判定为首次安装，由脚本下载 release 安装包...")
        remote_tag, assets = get_latest_release()
        if not remote_tag:
            print("[ERROR] 无法获取 GitHub 最新版本信息，安装中止。")
            sys.exit(1)
        if not is_native_version(remote_tag, allow_tag=True):
            print("[ERROR] 仓库尚无符合版本标识的原生评分发布包，请先从源码构建。")
            sys.exit(1)
        print(f"[LOG] GitHub 最新版本为: {remote_tag}")
        ok = install_from_archive(remote_tag, assets, binary_name, binary_path, config_path, version_file)
        sys.exit(0 if ok else 1)

    # 旧上游二进制仍内置上游下载源，不能委托它更新本分支。
    print(f"[LOG] 当前 VERSION 记录: {read_local_version(version_file)}")
    current = query_binary_version(binary_path)
    if current:
        print(f"[LOG] 二进制当前版本: {current}")
    if not is_native_version(current):
        print("[ERROR] 当前程序不是带 -grails 标识的原生评分版本，已停止自动更新。")
        print("[LOG] 请先从 Grails125/ncmm 构建或安装本分支发布包，再使用此更新脚本。")
        sys.exit(1)
    print("[LOG] 交由 ncmm 自身的更新规则执行（ncmm update --apply），脚本仅做手动触发...")

    update_code = update_with_ncmm(binary_path, binary_name)
    if update_code != 0:
        print("[ERROR] 更新失败，更新中止。")
        sys.exit(update_code)

    final_version = query_binary_version(binary_path)
    if not is_native_version(final_version):
        print("[ERROR] 更新命令已结束，但无法确认当前程序为本分支版本；已保留 VERSION 记录及恢复备份，请检查后重试。")
        sys.exit(1)
    write_local_version(version_file, final_version)
    # 恢复备份由 Go 更新器管理；脚本不删除可能仍被占用或用于恢复的 .old 文件。
    print(f"[SUCCESS] 更新流程完成，当前版本: {final_version}")
    print("[LOG] 提示：如正在运行 ncmm web / 面板服务，请重启后使用新版本。")

if __name__ == '__main__':
    main()
