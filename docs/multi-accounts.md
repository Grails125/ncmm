# 👥 多账号隔离最佳实践

在多账号运作或需要使用多个辅助号为自己（音乐人主账号）刷播放量的场景下，使用 `--home` 指定实例目录。配置中的相对 Cookie、数据库、日志和本地素材路径以最终实例 home 为基准，程序不会修改调用者的工作目录；YAML 中仍保留用户填写的路径。推荐目录结构如下：

```text
run/
├── config.yaml       # 多账号公共/专用配置文件（配置了顶级 accounts 以及 sign/mixPlay）
├── cookie.json       # 主账号 Cookie
├── fan1.json         # 辅助账号 1 Cookie
├── fan2.json         # 辅助账号 2 Cookie
├── database/         # 业务缓存与进度
└── log/
    └── ncm.log       # 运行日志
```

优先级为显式 `--home`、`NCMM_HOME`、默认或自动发现的实例目录。自动发现配置时以所选配置所在目录作为 home；显式 `--config` 只选择配置文件，不改变已选 home。`login cookie -f run/fan1.txt` 等显式输入文件仍相对调用时的工作目录读取，登录输出和配置内的数据路径归属实例 home。使用新 home 时先复制需要迁移的文件，不会自动搬动旧目录中的账号或数据库。

---

## 多账号实战操作示例：

### 1. 第一步：创建工作目录并放入初始配置文件
在本地建立文件夹 `run/`，并将默认的 `config.yaml` 复制进去，我们可以留空 `accounts` 节点，后续登录时系统会自动帮我们回写补全。

### 2. 第二步：登录/导入主账号
主账号为网易云音乐人账号。我们**必须显式提供 `-m` 参数**，以便让程序将其作为主账号处理并根据 `accounts.main` 的路径（或默认的 `cookie.json`）保存：
```bash
# 示例 1：以扫码形式登录主账号（推荐）
ncmm --home run login qrcode -m

# 示例 2：使用 Cookie 导入主账号
ncmm --home run login cookie 'MUSIC_U=xxxx...' -m
```
*登录成功后，主账号 Cookie 路径（例如 `cookie.json`）与对应昵称注释会自动回写并绑定在 `run/config.yaml` 的 `accounts.main` 下。*

### 3. 第三步：登录/导入各辅助账号
辅助账号为刷播放量的粉丝小号。所有的登录子命令默认即登录为辅助账号，无需特别指定。
我们只需使用 `-f` 导入（或直接登录），程序会**自动根据输入文件名推导目标 `.json` 文件名并保存，同时自动追加路径回写至 `run/config.yaml` 中的 `accounts.secondary` 配置列表中**，无须手动编辑配置文件：
```bash
# 示例 1：导入小号 fan1.txt，系统会自动推导并保存为 fan1.json 并回写配置
ncmm --home run login cookie -f run/fan1.txt

# 示例 2：导入小号 fan2.txt
ncmm --home run login cookie -f run/fan2.txt

# 示例 3：如果希望手动强制另存为特定文件名，依然可以使用 -o 指定
ncmm --home run login cookie -f run/other.txt -o another_fan.json
```
*每一次导入/登录成功后，`config.yaml` 中都将自动增加对应行的 `# 昵称: xxx` 注释，帮助直观辨别各账号。*

### 4. 第四步：一键运行签到与黑胶进阶任务
```bash
# 执行日常一键签到（会根据 sign 配置自动对主账号及所有辅助账号轮询签到）
ncmm --home run sign

# 执行每日音乐人签到与云豆领取（推荐每日自动运行）
ncmm --home run musician-sign

# 执行黑胶会员进阶任务（包含图文发布与辅助号接力刷播，可按需或每月自动运行）
ncmm --home run musician-vip

# 兼容一键执行上述日常与进阶任务
ncmm --home run musician
```
