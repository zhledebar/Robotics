# PriceStockMonitor V8.27 修复验证版

V8.26 诊断里，Dell 16 Pro 的报价已读到，但配置扫描仍在等待；XPS 每轮又先后切换普通配置和定制页面，拖慢了下一次配置检查。V8.27 让待检查商品轮流获得配置扫描，并让 XPS 配置轮次直接留在定制页面。配置轮次只检查一项代表配置，保留普通报价和现有预配置结果。

- [下载 Windows 程序 ZIP](https://github.com/zhledebar/Robotics/raw/refs/heads/main/downloads/PriceStockMonitor_Win64_V8.27.zip)
- [下载源码与测试记录](https://github.com/zhledebar/Robotics/raw/refs/heads/main/downloads/PriceStockMonitor_Source_V8.27.zip)

Go 回归为 337 通过、1 跳过；本机 Chromium 完成配置联动弹窗测试，Linux 应用与页面运行检查、Windows amd64 GUI 编译和三组 Node 检查通过。云环境无法执行 Windows UIAutomation，也无法访问 Dell 实时商品页，所以 Dell 16 Pro 的真实页面配置字段仍需用新版本验证；这次没有把本地模型结果当成实机通过。

先在旧版点击“退出程序”，再解压运行新版，原商品设置与历史沿用。详见[使用说明](使用与修复说明.md)和[验证记录](verification/verification_v827.json)。私有仓库下载需要登录有访问权限的 GitHub 账号。
