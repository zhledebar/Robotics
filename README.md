# PriceStockMonitor V8.25 修复验证版

先更新各商品报价，再检查 XPS 代表配置，避免 Dell Pro 被完整配置扫描长期阻塞。默认每轮最多切换 6 次，按弹窗联动后的实际配置核对；明确标记未穷举，可在编辑页选择完整遍历。

- [下载 Windows 程序 ZIP](https://github.com/zhledebar/Robotics/raw/refs/heads/main/downloads/PriceStockMonitor_Win64_V8.25.zip)
- [下载源码与测试记录](https://github.com/zhledebar/Robotics/raw/refs/heads/main/downloads/PriceStockMonitor_Source_V8.25.zip)

先在旧版点击“退出程序”，再解压运行新版。原商品设置与历史沿用。

Go 回归 333 通过、1 跳过；真实 Chromium 本地模型验证了生产配置遍历中的 6 次弹窗确认、4 次配置联动。应用界面和保存恢复测试通过。

尚未执行 Windows 采集或真实 Dell 网站测试，不能声明实机已经完全可用。代表配置可能漏掉未检查组合的折扣。

详见[使用与修复说明](使用与修复说明.md)和[验证记录](verification/verification_v825.json)。私有仓库下载需要先登录有访问权限的 GitHub 账号。
