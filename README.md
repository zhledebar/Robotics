# PriceStockMonitor V8.26 修复验证版

V8.25 诊断显示 XPS 连续切换六种配置会占用共用采集窗口约一分钟，Dell Pro 到期报价被推迟。本版每次只切换一项，核对弹窗联动后的实际配置后让出窗口；到期商品报价优先，再继续下一项。

- [下载 Windows 程序 ZIP](https://github.com/zhledebar/Robotics/raw/refs/heads/main/downloads/PriceStockMonitor_Win64_V8.26.zip)
- [下载源码与测试记录](https://github.com/zhledebar/Robotics/raw/refs/heads/main/downloads/PriceStockMonitor_Source_V8.26.zip)

快速代表配置会分轮检查，明确标记“未穷举”；需要遍历所有可选组合时，可在编辑页选择完整遍历。先在旧版点击“退出程序”，再解压运行新版，原商品设置与历史沿用。

本地回归 334 通过、1 跳过。真实 Chromium 本地网页模型通过一次弹窗确认及配置联动；报价调度、应用界面和重启恢复测试通过。尚未执行 Windows 实机采集或真实 Dell 网站测试。

详见[使用说明](使用与修复说明.md)和[验证记录](verification/verification_v826.json)。私有仓库下载需要登录有访问权限的 GitHub 账号。
