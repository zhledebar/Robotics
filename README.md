# PriceStockMonitor V8.24 修复验证版

## 下载

- [Windows 程序压缩包](https://github.com/zhledebar/Robotics/raw/refs/heads/main/downloads/PriceStockMonitor_Win64_V8.24.zip)
- [源码与测试记录](https://github.com/zhledebar/Robotics/raw/refs/heads/main/downloads/PriceStockMonitor_Source_V8.24.zip)

先在旧版点击“退出程序”，再解压并运行 V8.24 EXE。原商品设置与历史沿用。

本版修复 XPS 配置扫描中的重复编译与读取、无效内存切换等待、结果更新延迟，并补充当前配置库存核对和诊断。

本地 Go 回归：329 通过、1 跳过、0 失败。真实 Chromium 本地页面模型通过 40 组合及 63 次可信鼠标事件测试；Linux 应用流程和 Windows x64 GUI 交叉构建通过。

**当前为修复验证版：尚未运行 Windows UIAutomation、Windows PowerShell 缓存路径或真实 Dell 完整扫描。不能把本地测试结果当作实机全部可用证明。**

详见[使用与修复说明](使用与修复说明.md)及[验证记录](verification/verification_v824.json)。
