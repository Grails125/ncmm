# 第三方来源与许可

## NCMM 源码基线

本分支从 [AL8023/ncmm](https://github.com/AL8023/ncmm/tree/e60b1b19f302a50b1f983909d17b391e53245581) 保留的 v1.2.2 源码恢复构建，保留源文件中的版权声明及根目录 [MIT 许可证](LICENSE)。提交与版本边界见 [源码基线说明](docs/source-baseline.md)。

## 音乐合伙人流程参考

原生 Go 实现复用 NCMM 自有 API 客户端，并参考 [ACAne0320/ncmp](https://github.com/ACAne0320/ncmp/tree/0517539fa44d226a3b9cf7d6a68683f08aaa2ba3) 的评分策略及每日/额外任务流程。该参考项目使用 MIT 许可证，Copyright (c) 2025 ACAne0320，完整文本见 [licenses/ncmp-MIT.txt](licenses/ncmp-MIT.txt)。

用户提供的 `longyanbo1224-debug/ncmp_desktop` 使用独立的非商业许可证。本分支没有复制其桌面 UI、线程调度或其他专有增强代码；功能以 NCMM 原有 Go、WebUI 和调度结构重新实现。无需安装 Python 桌面应用或评分插件。
