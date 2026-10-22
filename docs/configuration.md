# 配置文件说明

完整字段、中文注释和当前默认值以 [config/config.yaml](../config/config.yaml) 为准；不要用旧版文档中的整份示例覆盖已有配置。程序版本保持 `1.2.2-grails.1`，配置兼容范围及迁移规则见 [兼容性说明](v129-compatibility.md)。

## 位置与保存

优先使用 WebUI“策略配置”，也可以通过 `--config` 指定文件。省略时先查实例 home，再在有限目录中查找标准文件名；显式 `--home`、`NCMM_HOME` 和 Docker `/data` 保持隔离。查找顺序、坏配置处理及恢复入口见 [命令行说明](cli.md)。手工修改后重启对应服务使配置生效。

## 配置分组

| 配置段 | 用途 |
|---|---|
| `accounts` | 主辅助账号 Cookie 文件与按账号映射的移动端 Token |
| `network` | 超时、重试与各协议 UA；移动请求需与登录会话匹配 |
| `task` | 批量开关、账号执行模式及快慢任务队列 |
| `musicPartner` | 原生音乐合伙人评分范围、额外目标、等待范围和五档乐评 |
| `sign` | 云贝、普通 VIP 签到与各日常子任务 |
| `playids` | 歌曲、文件、歌单来源以及每日/单次目标和间隔 |
| `mixPlay` | 日推混听比例及是否占目标额度 |
| `musician` | 音乐人签到、身份缓存、VIP进阶与专属播放覆盖 |
| `note`、`dailySongShare` | 图文素材、每日推歌、活动与话题 |
| `fansgroup` | 乐迷团账号、群ID、独立播放会话和评论短效凭据 |
| `vipMemberGift` | 会员礼品领取、主辅助赠送及互助服务 |
| `updater` | 原生发布检查、下载、空闲重启与代理镜像 |
| `notify` | 失败汇总策略；通道凭据另存 `notify.yaml` |
| `log`、`database` | 日志轮转与业务缓存位置 |
| `play.webSession` | 仅兼容历史字段，已废弃，运行时始终关闭 |

新建配置的日目标为80～200首、单次40～80首；辅助账号普通签到和乐迷团默认关闭，会员礼品领取默认开启。批量音乐人VIP默认开启，每日推歌和乐迷团默认关闭。辅助赠送仍需单独开启 `enableGiftSecondaries`，默认不会赠送。现有实例升级保留原有任务开关、目标、账号、歌曲和乐评，不套用这些新默认值。

`musicPartner` 的评分、额外目标与等待均支持范围；上下限相同表示固定值。分值乐评和积分展示详见 [音乐合伙人说明](music-partner.md)。应用预设不清空已有范围和乐评。

## 歌曲与音乐人歌单

`playids.ids`、`idsFile`、`playlistIds` 合并去重。歌曲文本支持中英文逗号、分号、空白和 `#` / `//` 注释；歌单支持ID及官方分享链接，由每个实际听歌账号独立读取，权限不足的来源不保证可用。

音乐人专属 `musician.play.ids` 或 `idsFile` 任一非空时使用该专属来源；两者都为空时继承全局歌曲、文件及歌单。歌单解析后的主歌池参与有效播放上报计数，混听按现有 `countTarget` 规则处理；最终是否达标以官方进度为准。

## 历史会话与评论

普通播放保留账号原 `os` Cookie，不通过 `play.webSession` 开启网页会话。此历史字段保留解析兼容，在WebUI标记为已废弃且不可开启。

仅乐迷团播放受 `fansgroup.webSession` 控制，默认false。关闭时跳过播放，其他已启用任务继续。开启后使用独立网页会话、检查 `authSecret`、完整下载并复查官方进度；请求成功不代表计数成功。官方1.2.8之前的配置首次导入时会安全关闭旧乐迷团网页会话，转换为本分支配置后用户重新开启的值会保留。

评论需要 `fansgroup.commentCheckTokens` 中对应账号正常网页请求的短效凭据；缺失或失效时跳过，不使用移动端Token替代。创建后数秒清理，删除失败保留恢复记录。目标、取消和不确定响应处理详见 [协议与验收边界](fansgroup-protocol.md)。

## 更新与发布策略

```yaml
updater:
  check: true
  auto_update: true
  auto_restart: true
  proxy_mirrors:
    - "https://gh-proxy.com/"
    - "https://ghproxy.net/"
    - "https://githubproxy.cc/"
```

更新只接受 `Grails125/ncmm` 的原生发布。`check:false` 关闭自动联网检查，但不会跳过尚在24小时有效期内的已验证升级策略缓存。无效、损坏、过期或检查失败的策略放行业务；普通版本不会凭空要求强制升级。

`auto_update` 控制后台下载，受Launcher管理的容器也支持；Compose环境变量默认显式设为false，手动检查/安装仍可使用。`auto_restart` 只在安装后且运行、排队、随机延迟任务全部结束时生效；手动更新有30秒确认时间。旧无启动器容器需重建，详见 [更新与发布](native-updates.md)。

发布流程先固定提交，再进行完整质量检查，成功后包和镜像并行构建。具备流程不等于已有发布包；尚无符合原生版本标识的Release时从源码构建。

## 日志与通知

新建配置的日志单文件轮转上限为5MB，旧实例已有自定义大小保留。`maxage` 控制保留天数，`maxbackups` 控制轮转日志数量；这与配置升级保留最近5份备份是不同的设置。

失败通知的通道、启用条件与凭据说明见 [通知配置](notify.md)。业务配置中的 `notify.enabled` 控制失败汇总，`on_skip` 决定跳过是否纳入；通道凭据放独立 `notify.yaml`，不要在任务日志中输出。

## 配置写入与并发

从 `v1.2.0` 开始，程序通过 `config.yaml.lock` 协调 WebUI、任务子进程和独立 CLI 进程，并使用同目录临时文件原子替换 `config.yaml`。读取方只会看到完整旧配置或完整新配置；锁文件长期存在是正常现象，它不包含凭据，也不代表进程仍在运行。

WebUI 还会在规范化后的 home 中维护 `webui.instance.lock`。它使用 Linux `flock` 或 Windows `LockFileEx` 保证一个 home 同时只有一个 WebUI 控制实例，并保存 PID、启动时间、监听地址、版本和 instance ID 供启动冲突诊断。该文件退出后不会删除，但 OS 锁会在正常或异常退出时自动释放；不要根据文件是否存在判断 WebUI 是否运行，也不要在实例运行期间手工覆盖它。

管理员认证独立保存在 `webui-auth.json`，不使用业务 Badger 数据库。文件只包含 PBKDF2 密码 hash、SHA-256 Session Token hash、密码/会话策略和会话元数据，不包含明文密码或明文 Session Token；`webui-auth.json.lock` 用于跨进程事务协调。浏览器 Session Cookie 为 HttpOnly、SameSite=Strict，默认 7 天绝对有效期和 1 小时服务端空闲超时，可在 WebUI“系统”页调整；最多保留 128 个会话。认证文件损坏时 WebUI 会安全拒绝启动，不会退化为无密码模式；停止实例后可用 `ncmm auth reset-password` 直接替换，或用 `ncmm auth clear --yes` 清除并重新设置。

v1.2.0 将 WebUI 认证视为全新状态，不读取或迁移旧版管理令牌。没有 `webui-auth.json` 时进入首次设置；此过程以及 `auth reset-password` / `auth clear` 仅操作认证文件，不会修改 `config.yaml`、Cookie、业务数据库、`webui.yaml` 调度规则或 `log/runs` 运行记录。

WebUI 获取配置时会同时收到内容哈希 `revision`。保存配置必须提交该 revision：缺失时 API 返回 `428 Precondition Required`；配置已被其他页面、登录流程或 CLI 更新时返回 `409 Conflict`，此时应重新加载后再保存。每次 WebUI 保存仍会原子更新 `config.yaml.bak` 作为人工恢复副本。

Cookie 导入和二维码登录由子进程完成登录验证与 Cookie 落盘，但 `accounts` 更新由 WebUI 父进程统一提交。若登录期间配置发生变化，Cookie 文件会保留，页面会明确提示配置提交冲突，不会覆盖较新的配置。

