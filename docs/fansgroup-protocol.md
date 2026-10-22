# 乐迷团播放与评论：协议和验收边界

此功能内置于 `fansgroup`，与音乐合伙人评分、普通 `playids`、音乐人有效播放分离。

## 配置与运行

- `fansgroup.webSession` 默认 `false`。关闭时只跳过乐迷团播放，点赞、分享、笔记及已支持的加速任务继续执行。开启只影响乐迷团播放地址获取和专用上报：请求临时过滤 `os`，不改账号持久化 Cookie，不影响普通播放。
- `fansgroup.commentCheckTokens` 默认空。按 Cookie 文件路径配置**正常网页评论请求正文中的短效 `checkToken`**，不能填写 `accounts.antiCheatTokens` 中的移动端凭据。WebUI 以敏感字段展示。新建评论缺失此凭据时安全跳过；程序不生成 Watchman 数据、不处理或绕过验证码。短效凭据可能很快失效，不保证长期无人值守评论。
- 评论仅使用加速任务明确提供且唯一的视频 `threadId`、`videoId`，或通过只读转换接口确认的 `mlogId`。不从通用 `id`、歌曲、推荐流猜测目标。未知/冲突目标跳过，不发表评论。
- 播放媒体必须来自受支持的网易云 CDN，且带非空 `authSecret`。音频完整流式读取，不保存文件、不带账号 Cookie；拒绝部分响应和截断，限制为 100 MB / 2 分钟，取消可中断。取消原有仅探测 512 KiB 的行为。无需整首等待，按实际拉取耗时上报；这不代表完整收听，也不改普通刷歌的整首等待。
- 每轮请求结束后重新读取任务进度，日志展示前后值。接口 `200` 只说明接受请求；实际任务是否完成以刷新后的官方进度为准。刷新失败明确报错。

## 评论删除与恢复

每账号在 `<home>/fansgroup-comment-cleanup/<UID>.json` 保存一个清理记录，权限为 `0600`，内容只有账号 UID、视频 threadId、评论 ID、创建时间、删除次数及数值状态码，不保存 Cookie、正文或 Token。使用账号级文件锁和原子写入，防止重叠任务同时发表评论。

先持久化创建意图，再发出一次评论请求；明确获得评论 ID 后立即持久化，等待 3–7 秒后删除。任务取消会尽快进入最多 15 秒的清理窗口。删除失败保留记录；同账号下次执行乐迷团时优先重试删除，成功才移除记录。未清理记录会阻止新评论。

网络中断、服务端异常或成功响应缺少评论 ID 时，可能已经创建评论。此时保留 `commentId: 0` 的未知记录，不重复发送创建请求；请在官方评论区核对目标和时间，将确认属于该账号的评论 ID 补入记录后再执行恢复，或确认评论已删除/根本不存在后清理该记录。不要填入其他评论 ID。强制结束进程、磁盘损坏、响应丢失等情形不能保证立即删除，也不能承诺评论不会短暂可见。

官方网页将 `407` 也显示为评论成功。因此有明确评论 ID 时仍执行删除，缺少 ID 时保留未知记录；不能把它当作普通拒绝而丢弃记录。明确的限频、权限、资源不存在等拒绝会结束本次尝试，不自动重发。

## 协议出处（2026-10-22 只读核对）

1. [网易云官方网页 core 脚本](https://s3.music.126.net/web/s/core_c7686f506f6f2607bb7f6cea1951f039.js)：`eI0u` 设置 `mainsite`、`mainsiteWeb`；`Rg8m` 记录实际秒数、结束原因、来源；请求包装器把 weblog 域名改为 `clientlogusf.music.163.com`；播放器使用 `/api/song/enhance/player/url/v1`。`kJ0u` 经正常网页 Watchman 实例获取评论校验值。
2. [网易云官方网页播放框架脚本](https://s3.music.126.net/web/s/pt_frame_index_2b8192306764f32086606cd3d6d62d64.js)：切换歌曲上报 `startplay`，包含歌曲 ID、类型和页面查询内容。
3. [网易云官方网页评论脚本](https://s3.music.126.net/web/s/pt_song_index_d0a152c42c4ce7550652d601df8db69c.js)：新建使用 `/api/resource/comments/add`，正文为 `threadId`、`content`、`checkToken`，响应读取 `comment.commentId`；删除本人评论使用 `/api/resource/comments/delete` 与 `threadId`、`commentId`，不获取新建时的校验值。创建调用等待正常网页 SDK 回调，不据此推断服务端允许无 Token；本实现默认不发送缺凭据的新建请求。
4. [go-musicfox 的固定版本参考](https://github.com/go-musicfox/netease-music/blob/912f2cf2b8bf772c804c6cd4b4f31f11773c7b93/service/scrobble_service.go)：验证 `clientlogusf` 的 WEAPI 路径、`logs` 序列化及 CSRF 参数。本项目使用专门方法，不替换普通 `WebLog`。
5. [api-enhanced 资源类型映射](https://github.com/NeteaseCloudMusicApiEnhanced/api-enhanced/blob/19c7dad6db7d660954c7a615a686ff0acb030f91/util/config.json)及 [mlog 转视频接口](https://github.com/NeteaseCloudMusicApiEnhanced/api-enhanced/blob/19c7dad6db7d660954c7a615a686ff0acb030f91/module/mlog_to_video.js)：视频评论前缀为 `R_VI_62_`；`/api/mlog/video/convert/id` 用 `mlogId` 获取视频标识。任务按钮/详情的容错解析属于本项目实现，未声称覆盖尚未观察到的所有任务响应结构。
6. [api-enhanced 评论实现](https://github.com/NeteaseCloudMusicApiEnhanced/api-enhanced/blob/19c7dad6db7d660954c7a615a686ff0acb030f91/module/comment.js)另有 EAPI + v2 令牌方案。本项目**不采用该分支**，不把其生成令牌过程或移动端 `X-antiCheatToken` 与网页正文 `checkToken` 混用。

公开网页还存在 `new-bi-log/useNewEncrypt` 分支，社区也报告过 weblog 接受请求但普通听歌排行不增加。因此这里实现可核对的字段、域名与现有 WEAPI 传输，并保留真实进度复查；不能根据旧参考或模拟 `200` 承诺当前账号/地区的乐迷团计数。

## 验证范围

自动测试使用隔离临时目录和模拟 HTTP：会话过滤且不污染原 Cookie、普通任务保留 `os`、完整下载与截断/取消/目标校验、默认关闭播放但其他任务继续、评论协议/不重试、删除失败恢复、取消后删除、未知创建阻止重复、损坏/跨账号/加锁记录保护及配置迁移保留。

本组未使用真实账号发评论、上报播放、评分或赠送。真实账号任务进度验收尚未执行；评论短效凭据有效性与服务端实际计数须另行验证，不将模拟协议测试视为官方任务已完成。
