# 音乐合伙人原生评分

当前版本 `1.2.2-grails.1` 基于可公开获得的 NCMM v1.2.2 完整源码。评分运行在同一个 Go 主程序中，使用原有账号 Cookie、网络客户端、WebUI、批量任务、调度器和失败通知。无需安装评分插件或 Python 桌面程序。源码来源和版本限制见 [source-baseline.md](source-baseline.md)，许可见 [第三方说明](../THIRD_PARTY_NOTICES.md)。

## Docker 启用

1. 在本分支执行 `docker compose up -d --build`，访问 `http://127.0.0.1:3899`，设置管理密码。
2. 在账号页面登录有音乐合伙人资格的网易云账号。Cookie 需要包含 `MUSIC_U` 与 `__csrf`，过期时应重新登录。
3. 在配置页面的“音乐合伙人评分”设置账号范围、评分策略和额外任务，再开启批量任务中的“音乐合伙人评分”。初始 `task.music-partner` 为关闭。
4. Compose 的 `CRON_1="30 8 * * * task"` 每天北京时间 08:30 运行配置中已启用的任务。需要不同时间或分开调度时，可删除该环境变量，在原有任务页面创建内置 `music-partner` 任务。避免同时保留重复定时规则。
5. 先通过任务页面手动执行一次，查看运行状态和日志。通知沿用 `notify.yaml` 与全局通知开关。

已有较新上游实例请先备份并使用新的数据目录；程序会拒绝将大于 v1.2.2 的配置自动降级写回。本仓库尚未发布镜像时应按 [Docker 文档](docker.md) 从源码构建，不能换用上游 `3899/ncmm` 镜像。更新二进制仅接受本仓库的 `*-grails.N` 发布。

## 命令行

```bash
# 按 musicPartner.enableMain / enableSecondaries 选择账号
ncmm --home ./run music-partner

# 明确执行单个账号（不受批量总开关影响）
ncmm --home ./run music-partner --cookie-file ./run/cookie.json

# 只选择批量任务中的音乐合伙人评分，仍遵守主/副账号开关
ncmm --home ./run task --music-partner

# 执行全部已启用的批量任务
ncmm --home ./run task
```

WebUI 单账号任务使用已有的 `--cookie-file` 参数，调度时会与同一账号的签到等任务互斥。批量任务仍使用原有的保守资源锁。独立从终端启动的多个进程不共享 WebUI 调度锁，不应同时对相同账号执行评分。

## 配置

```yaml
task:
  music-partner: true

musicPartner:
  enableMain: true
  enableSecondaries: false
  score: 3
  extraTasks: true
  fullExtraTasks: false
  extraLimit: 7
  waitMin: 15
  waitMax: 20
  syncYunCircle: false
```

旧版本的自定义快/慢队列会保留。只要启用 `task.music-partner`，新任务会在缺少显式排序项时自动追加到慢任务组；`task --only-fast` 不执行该默认慢任务。

| 配置 | 行为 |
| --- | --- |
| `score: 1` | 名称或作者含 ASCII 英文字母给 2 分，否则给 1 分 |
| `score: 2` | 含英文字母给 3 分，否则给 2 分 |
| `score: 3` | 含英文字母给 4 分，否则给 3 分（默认） |
| `score: 4` | 固定 4 分 |
| `extraTasks` | 是否在基础任务后处理额外作品 |
| `fullExtraTasks: false` | 根据接口返回的已完成记录补到每日累计 `extraLimit`，默认 7 首 |
| `fullExtraTasks: true` | 处理本次接口返回的全部待评额外作品，不无限拉取 |
| `waitMin` / `waitMax` | 每次提交前随机等待，单位秒，范围 1–300，默认 15–20 |
| `syncYunCircle` | 是否将评分同步云圈，默认关闭 |

这是参考项目的规则评分，不进行听音、音频质量分析或 AI 音乐评价。每日额外进度以平台响应为准；如果平台不再返回完整的当日已完成记录，累计限额语义需要重新适配接口。

## 失败与恢复

- 每次执行读取平台任务进度，跳过已完成作品。额外作品先提交播放完成报告，报告成功才提交评分。
- 网络写入失败不自动重投。先查询一次完成状态；无法确认时返回失败，避免把结果未知的提交当作成功。
- 未登录、无资格、频率限制、无效响应、资源报告失败与部分评分失败会进入失败日志和通知汇总。单独命令以及包含评分的批量命令返回非零退出码，WebUI 显示失败。
- 取消任务会中止等待和 HTTP 请求。已经被平台接受的提交不会因本地取消而撤回，下次运行会重新查询。
- 任务运行到北京时间次日零点时会停止当前快照，避免将前一天任务 ID 用于新一天的评分；下次调度重新读取任务。
- 评分请求强制证书校验、有限超时和禁止自动 POST 重试；即使开启全局调试，也不记录评分请求的 Cookie、CSRF 或原始响应。

仓库测试使用模拟接口与隔离数据目录；不包含真实账号的资格、实时接口可用性、实际评分入账或云圈同步验收。
