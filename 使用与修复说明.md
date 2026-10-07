# PriceStockMonitor V8.26

V8.25 诊断显示，XPS 普通报价更新后，六次配置切换连续占用了采集窗口；Dell Pro 到期报价要等这一整批结束。本版将代表配置检查拆成单次切换：确认弹窗、记录实际联动配置后立即归还窗口；若有报价到期，先读取报价，再继续 XPS 下一项。

- 默认快速代表配置，每次只做一次选项切换。弹窗联动后的实际 CPU、显卡、内存、硬盘、屏幕、价格和库存核对并去重。
- 配置检查在未穷举时轮换候选项，仍明确显示“未穷举”。需要完整遍历时可在商品编辑页选“完整遍历（较慢）”。
- 初始报价阶段仍先完成全部商品的普通报价与当前定制，才开始抽查其他选项。
- 保留读取组件缓存、未确认分支处理、库存绑定检查与扫描结果渐进展示。

## 本地验证

- Go 全量回归：334 通过、1 跳过、0 失败。跳过项需要额外捕获页面。
- Chromium 本地 Dell 页面模型运行生产配置遍历与弹窗确认代码：一次选项切换、一次弹窗确认、一次联动，记录两个经过核对的实际配置。
- 新增调度回归：XPS 下一项待检查时若 Dell Pro 报价到期，先安排 Pro 报价，再继续核心配置抽查。
- 新增代码的 race 检查、Linux/Windows `go vet`、Windows amd64 GUI 交叉构建通过。
- Linux 实际应用和 Chromium 界面：报价配置模式编辑保存、诊断导出、商品增删改、退出重启恢复通过。三组 Node 页面模型检查通过。

云环境为 Linux；没有执行 Windows UIAutomation/PowerShell，也没有连接真实 Dell 商品页（云代理拒绝该请求）。本地模型测试不能证明 Windows 实机及 Dell 页面已完全可用。

## 安装

在旧版界面点击“退出程序”，解压 Windows ZIP 并运行 PriceStockMonitor_Win64_V8.26.exe。原设置和历史沿用。程序会保存当前设置。

详细证据见 verification/verification_v826.json、verification/go-tests-v826.jsonl 和 verification/coupled-browser-v826.log。V8.25 的历史验证记录保留供对照。
