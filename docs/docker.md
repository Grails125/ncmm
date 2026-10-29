# Docker 部署

本分支将音乐合伙人评分编译进 `ncmm` 主程序，与账号、批量任务、定时任务、日志和通知共用同一套功能。镜像只运行 Go 程序，不需要 Python、桌面环境或评分插件。

当前构建版本为 `1.2.7-grails`，源码恢复自保留完整代码的 v1.2.2 fork，**不是上游 v1.2.9 的完整源码**。已补齐的公开功能及配置迁移边界见 [源码说明](source-baseline.md) 和 [兼容性说明](v129-compatibility.md)。已有 v1.2.9 部署先保留原镜像和完整数据备份，在独立目录验证，勿让两个实例同时操作同一数据目录。

## 构建并启动

安装 Docker Engine 和 Docker Compose v2，在本仓库根目录执行：

```bash
docker compose up -d --build
docker compose logs -f ncmm
```

Compose 从当前源码构建 `ncmm-native:local`，不会下载 `ghcr.io/3899/ncmm:latest` 覆盖内置评分功能。默认只在宿主机 `127.0.0.1:3899` 发布 WebUI 端口；服务器可通过 SSH 端口转发访问，或在完成首次密码设置后配置 HTTPS 反向代理。

默认配置：

| 项目 | 设置 |
| --- | --- |
| WebUI | `http://127.0.0.1:3899` |
| 时区 | `Asia/Shanghai` |
| 持久目录 | 仓库下 `./data` 映射到容器 `/data` |
| 自动运行 | 每天北京时间 08:30 执行 `ncmm task` |
| 更新方式 | 更新仓库源码后重新构建镜像 |

运行目录保存配置、Cookie、认证、任务记录和进度。一个 `/data` 目录只交给一个 WebUI 实例，避免多套调度器重复执行任务。

## 首次设置和内置评分

1. 打开 WebUI，设置管理员密码。
2. 在账号中心扫码或导入 Cookie，按需要登记主账号和辅助账号。评分账号必须具有音乐合伙人资格。
3. 在策略配置中选择所需的现有任务，并开启批量任务中的“音乐合伙人评分”。
4. 设置账号范围、评分范围、额外评分目标和等待时间。默认每首在 3–4 分之间随机取整数，不分析音频质量；额外任务默认目标为当天累计 5–15 首之间选取的目标（范围允许 0–15，目标为 0 时跳过），也可处理本次列表全部待评作品。1–5 分可分别配置乐评池，留空则不提交乐评。
5. 保存后可先单独运行“音乐合伙人评分”检查结果。完成设置后，默认 08:30 的批量规则会自动包含已启用的评分任务。

对应 YAML 字段（仅展示评分部分，其余已有配置保留）：

```yaml
task:
  music-partner: true

musicPartner:
  enableMain: true
  enableSecondaries: false
  scoreMin: 3
  scoreMax: 4
  extraTasks: true
  fullExtraTasks: false
  extraLimitMin: 5
  extraLimitMax: 15
  waitMin: 15
  waitMax: 20
  syncYunCircle: false
  comments:
    "1": []
    "2": []
    "3": []
    "4": []
    "5": []
```

评分任务启用后应位于 `task.slow_tasks` 列表中，名称为 `music-partner`。默认模板包含此项；已有自定义队列需保留或补入此项。新功能不会自动改写其他任务的业务开关。Cookie 过期、没有合伙人资格或其他任务缺少移动端凭据时，需要在原账号/配置页面处理，Docker 不会绕过这些条件。

## 定时规则

Compose 默认注入 `CRON_1: "30 8 * * * task"`。它由 WebUI 内置 Go 调度器执行，在“定时任务”页面显示为环境变量托管的只读规则。容器启动不会立即执行所有任务。

如果要在 WebUI 自由调整计划，删除 Compose 中的 `CRON_1`，执行 `docker compose up -d` 重建容器，再创建命令为 `task` 的定时规则。不要同时保留重复规则，也不要额外启动 `crond`、青龙或另一套宿主机 cron 执行同一批任务。

可选的独立评分规则使用命令 `music-partner`。如果批量 `task` 已包含评分，一般无需再安排同账号独立评分。

## 手动运行和诊断

```bash
# 查看内置评分命令
docker compose exec ncmm ncmm --home /data music-partner --help

# 只运行评分
docker compose exec ncmm ncmm --home /data music-partner

# 显式通过批量入口只运行评分
docker compose exec ncmm ncmm --home /data task --music-partner

# 查看容器内程序版本
docker compose exec ncmm ncmm --version
```

日常建议从 WebUI 启动和停止任务，以复用调度器的账号排队和日志管理。命令行诊断前确认同一账号没有正在运行的任务。已有 CookieCloud 用户可在 Compose 中填写对应变量；宿主机服务地址使用 `host.docker.internal`，同一 Compose 网络中的服务使用服务名。

## 更新与回退

```bash
git pull --ff-only
docker compose build --pull
docker compose up -d
docker compose exec ncmm ncmm --version
```

Compose 通过 `NCMM_UPDATER_AUTO_UPDATE=false` 默认关闭自动下载；受 Launcher 管理的容器仍支持手动暂存更新、重启健康确认和失败回滚，详见 [Docker 运行时](docker-runtime.md)。更新入口只指向 `Grails125/ncmm` 原生发布，不使用上游官方镜像替换本分支。每次更新前保留已验证的镜像标签，并在停止任务后备份整个 `data` 目录；回退程序时同时核对配置兼容性，不假定旧版本可以直接读取新版本数据。

非 Docker 安装的更新辅助脚本也只接受本分支 `x.x.x-grails` 或历史 `x.x.x-grails.N` 版本的二进制。上游旧二进制仍可能内置上游下载源，应先从本仓库源码构建或安装本分支发布包，不能通过调用旧程序的更新命令完成迁移。

项目提供手动或版本标签触发的镜像/二进制发布工作流。普通分支推送和 PR 只执行质量检查与 Docker 构建验收，不发布镜像或创建 Release。代码同步到 GitHub 不等于镜像已发布，更不等于服务器已经部署。
