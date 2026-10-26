# 锦鲤免费许愿

锦鲤免费许愿是日常签到中的独立子任务，使用平台当日提供的免费次数，不购买次数，也不调用付费许愿接口。许愿参与成功不代表中奖；奖品及开奖结果以官方页面为准。

## 配置与执行范围

`sign.enableKoiWish` 默认关闭。开启后仍需启用对应的 `sign.enableMain` 或 `sign.enableSecondaries`，并满足自动任务条件：`sign.automatic: true`，或直接运行 `ncmm sign --automatic`。许愿不依赖 `sign.enableVipTask` 开关；账号是否具备当前活动资格由官方返回的状态决定。

批量执行需启用 `task.sign`，或显式选择 `ncmm task --sign`。队列项为 `KoiWish`；开启许愿后，两个队列都没有此项时追加到快队列末尾，保留原有任务顺序和已有位置。`--only-fast`、`--only-slow` 仅执行所选任务组。直接 `ncmm sign` 不依赖队列。

实际提交使用当前账号配套的移动端 Cookie、UA 和 Token。Token 复用 `accounts.antiCheatTokens` 中按账号配置的移动端凭据，与网页评论使用的 `checkToken` 不同。缺少所需凭据或无法确定移动端协议时跳过，不伪造 Token，也不修改保存的 Cookie、UA 或 Token。

## 官方请求契约

协议依据为 2026 年 10 月 26 日从[网易云音乐官网 Android 下载入口](https://music.163.com/api/android/download/latest)获取的官方 APK，以及同日用户正常操作的脱敏请求与状态变化。下载入口是滚动更新地址，后续版本变更时仍需重新核对协议，不能仅凭 URL 相同认定客户端行为未变。

本次核对的[官方 APK 文件](https://d1.music.126.net/dmusic/NeteaseCloudMusic_Music_official_9.6.13.261022231853_32.apk)为 `NeteaseCloudMusic_Music_official_9.6.13.261022231853_32.apk`，大小 172,887,527 字节，SHA-256 为 `f8933f9f676e94b41013af4fdfdae1abdf82e9ef43df8308c690a11a4dcf9299`。请求逻辑来自内置 `rn-ad-free-music@index` Hermes 模块，Token 注入链来自 `classes14.dex`；仅作静态解析，未安装或执行 APK。

`getLotteryModuleList` 使用 POST 请求逻辑路径 `/api/vipnewcenter/app/lottery/module/list`，未设置业务参数。本项目发送空业务对象，不传入历史活动信息。

官方客户端的免费许愿调用使用逻辑路径 `/api/vipnewcenter/app/lottery/free/click`，业务活动字段为 `prizeTableId`，来自当前 `module/list` 返回的 `activityProcessInfo.prizeTableId`。不能硬编码历史活动 ID，也不能用其他活动的 `activityId` 代替。

官方 APK 的请求选项含 `needsGuardianToken: true` 和 `tokenKey: "token"`。原生桥接通过 `IAntiSpam.getToken` 获取移动端 Token，并将同一个值放入请求头 `X-antiCheatToken` 与请求业务正文 `token` 字段。`needsGuardianToken`、`tokenKey` 是客户端桥接选项，不是免费许愿接口的业务正文；不附加未经确认的 `checkToken`、次数或付费字段。

可复核的静态定位为：Hermes 函数 7823／7826 分别对应查询与提交，函数 10520 从模块响应取 `prizeTableId`；DEX 原生桥方法 29790 的指令单元 `0117–013f` 和方法 29910 的 `0044–0060` 均显示 `IAntiSpam.getToken()` 返回值同时写入头部与 `body[tokenKey]`。这些编号只适用于上述固定 APK。

本项目通过已有移动端加密请求链发送 EAPI／XEAPI，保持账号会话、UA 与移动端身份匹配。免费许愿写请求禁用自动重试，并将请求标记为敏感，避免输出 Token、Cookie 或完整请求正文。

## 三段状态证据

以下是用户提供的 2026 年 10 月 26 日请求记录的脱敏摘要，时间为北京时间；表中不保留账号、Token、Cookie、真实活动 ID 或中奖用户资料。

| 阶段 | 时间 | 请求路径与已确认结果 |
|---|---|---|
| 操作前查询 | 13:06:53.184 | `/xeapi/vipnewcenter/app/lottery/module/list` 返回 `code=200`；`vip=true`，`thisDayCanUseFreeCount=1`，`useEveryDayFreeCount=false`；`activityProcessInfo.status=0`，`visitorParticipateNum=0` |
| 用户提交免费许愿 | 13:07:00.409 | `/xeapi/vipnewcenter/app/lottery/free/click` 返回 `code=200`，`data.success=true`、`privilege=true`、`count=1` |
| 操作后查询 | 13:07:00.948 | 同一 `module/list` 返回 `code=200`；同一期活动的 `thisDayCanUseFreeCount=0`，`useEveryDayFreeCount=true`，`visitorParticipateNum=1` |

这组记录证明该次官方操作消耗一次免费机会并增加一次本期参与。它不能证明其他账号、其他活动或后续客户端版本具有相同资格，也不能把一次 `code=200` 单独视为许愿已完成。

## 结果确认与继续处理

1. 先查询当前活动和免费状态。今日免费次数已用过时直接跳过；字段缺失、活动不可用或资格不足时不提交。
2. 满足资格且移动端凭据可用时，使用实时活动 ID 最多提交一次免费许愿。
3. 提交后重新查询平台状态，核对同一期活动、免费次数与本期参与变化；未得到足够证据时记录“未确认”，不根据 HTTP 成功或请求回执猜测结果。
4. 网络超时、响应丢失或确认失败后不重发写请求。后续正常执行仍先读官方状态，避免已经完成的免费许愿被再次提交。

普通失败或未确认会进入任务结果汇总，其余可执行任务继续处理；取消和账号鉴权失效沿用任务调度器的停止规则。诊断只记录必要状态和稳定提示，不输出账号凭据。

## 验收边界

官方 APK 静态协议、用户操作记录、离线测试和本项目实时请求是不同证据。上述三段记录来自用户在官方客户端的操作，不是本项目自动提交的验收结果。本次本项目只读查询已验证官方返回“免费次数为 0、今日已使用、本期参与次数为 1”；这证明查询链路与当前状态，不证明本项目新的免费许愿写请求已在真实账号上执行成功。
