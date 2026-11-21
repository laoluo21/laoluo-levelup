#!/usr/bin/env python3
"""Prepare the laoluo-levelup Unix socket for a host Nginx on Linux/systemd."""

import argparse
import json
import os
from pathlib import Path
import pwd
import re
import stat
import subprocess


SOCKET_DIRECTORY = "admin-web"
SOCKET_FILENAME = "admin.sock"
MOUNT_POINT = Path("/run/laoluo_admin")
UNIT_NAME = "run-laoluo_admin.mount"
UNIT_PATH = Path("/etc/systemd/system") / UNIT_NAME


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def mounted_source(container):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", container):
        raise ValueError("请指定 Docker 容器名称或 ID")
    inspected = json.loads(command("docker", "inspect", "--type", "container", container))[0]
    if not inspected["State"]["Running"]:
        raise ValueError("请先在 Mengka 中启动插件")
    for mount in inspected["Mounts"]:
        if mount["Destination"] == "/data" and mount["RW"]:
            path = Path(mount["Source"])
            if not path.is_absolute() or not re.fullmatch(r"[a-zA-Z0-9/_.-]+", str(path)):
                raise ValueError("不支持此挂载路径")
            return path
    raise ValueError("此容器没有可写的插件 /data 挂载")


def prepare_directory(data, gid):
    path = data / SOCKET_DIRECTORY
    with_fd = os.O_DIRECTORY | os.O_NOFOLLOW | os.O_RDONLY
    directory = os.open(path, with_fd)
    try:
        socket_fd = os.open(SOCKET_FILENAME, os.O_PATH | os.O_NOFOLLOW, dir_fd=directory)
        try:
            directory_info = os.fstat(directory)
            socket_info = os.fstat(socket_fd)
            if (directory_info.st_uid != data.stat().st_uid
                    or socket_info.st_uid != directory_info.st_uid
                    or not stat.S_ISSOCK(socket_info.st_mode)
                    or stat.S_IMODE(socket_info.st_mode) != 0o660):
                raise ValueError("未找到符合要求的插件 Socket，请核对容器和插件版本")
            os.chown(f"/proc/self/fd/{socket_fd}", -1, gid)
            os.fchown(directory, -1, gid)
            os.fchmod(directory, 0o2710)
        finally:
            os.close(socket_fd)
    finally:
        os.close(directory)
    return path


def mount_unit(source):
    return f"""[Unit]
Description=laoluo-levelup admin socket directory
DefaultDependencies=no
Requires=docker.service
After=docker.service
Before=nginx.service umount.target
Conflicts=umount.target

[Mount]
What={source}
Where={MOUNT_POINT}
Options=bind,ro,nosuid,nodev,noexec

[Install]
WantedBy=multi-user.target
"""


def nginx_location():
    return f"""location = /laoluo-levelup {{
    return 308 /laoluo-levelup/;
}}
location ^~ /laoluo-levelup/ {{
    proxy_pass http://unix:{MOUNT_POINT}/{SOCKET_FILENAME}:/;
    proxy_http_version 1.1;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_cookie_path / /laoluo-levelup/;
}}"""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("container", help="运行 laoluo-levelup v0.3.5 或更新版本的 Docker 容器")
    parser.add_argument("--nginx-user", default="www-data", help="Nginx 工作进程用户（默认 www-data）")
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("请使用 sudo 运行")
    if not Path("/run/systemd/system").is_dir():
        parser.error("安装工具需要 systemd。非 systemd 环境请手动挂载 Socket 子目录")
    try:
        data = mounted_source(args.container)
        user = pwd.getpwnam(args.nginx_user)
        if user.pw_uid == 0:
            raise ValueError("请选择非 root 的 Nginx 工作进程用户")
        source = data / SOCKET_DIRECTORY
        text = mount_unit(source)
        if UNIT_PATH.is_symlink() or (UNIT_PATH.exists() and UNIT_PATH.read_text() != text):
            raise ValueError(f"已存在其他挂载配置 {UNIT_PATH}，请先检查")
        if MOUNT_POINT.is_symlink() or (os.path.ismount(MOUNT_POINT) and not MOUNT_POINT.samefile(source)):
            raise ValueError(f"{MOUNT_POINT} 已被其他挂载占用")
        prepare_directory(data, user.pw_gid)
        if not UNIT_PATH.exists():
            with UNIT_PATH.open("x") as unit:
                unit.write(text)
        subprocess.run(["systemctl", "daemon-reload"], check=True)
        subprocess.run(["systemctl", "enable", "--now", UNIT_NAME], check=True)
        endpoint = MOUNT_POINT / SOCKET_FILENAME
        status = json.loads(command("runuser", "-u", args.nginx_user, "--", "curl",
                                    "--fail", "--silent", "--show-error", "--max-time", "5",
                                    "--unix-socket", str(endpoint), "http://localhost/api/session"))
        if not isinstance(status.get("setup_required"), bool) or not status.get("version"):
            raise ValueError("上游返回内容不符合插件管理接口格式")
        print(f"Socket 连通，插件版本 v{status['version']}。")
        print("把以下片段加入现有 Mengka HTTPS 站点的 server {}，检查并重载 Nginx。\n")
        print(nginx_location())
    except (OSError, ValueError, KeyError, IndexError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"无法接入管理页：{error}\n")


if __name__ == "__main__":
    main()
