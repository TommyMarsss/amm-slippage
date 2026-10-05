# amm-slippage — 恒定乘积 AMM 交易执行、滑点保护与三明治攻击检测

用 Go 标准库（`math/big`）实现 Uniswap v2 核心模型 x·y=k 的**精确整数**交易
执行、滑点保护、价格冲击度量与三明治攻击（sandwich attack）检测，并把一组
交易序列渲染成**单一静态 HTML 报告**（数据与逻辑全部内嵌、零第三方依赖、
无需启动服务，浏览器直接打开）。

- Go 端只允许标准库；所有金额/储备为 128 位级 `big.Int`，定价全程无浮点。
- 前端为原生 HTML/CSS/JavaScript + 手写 SVG，无任何框架或图形库。

## 快速开始

```bash
go run ./cmd/ammdemo                 # 运行三组场景并生成 report.html
go run ./cmd/ammdemo -o /tmp/x.html  # 指定输出路径
go test ./...                        # 自动化测试
go vet ./...
```

终端会逐笔打印成交/拒绝、价格冲击、k 值增长与三明治损益；随后用浏览器打开
生成的 `report.html` 即可查看价格曲线、被拒交易高亮与疑似攻击时间窗。

## 目录结构

```
amm/
  pool.go      # 资金池、恒定乘积报价、滑点保护、交易执行（big.Int 精确整数）
  scenario.go  # 交易序列运行器（含“把建仓所得全额回投平仓”的引用解析）
  detect.go    # 异常跳变检测 + 不依赖标签的三明治形态启发式 + 损益核算
  report.go    # 报告数据模型与单文件 HTML 注入
  template.html# 报告前端模板（原生 SVG 图表/表格/面板，go:embed 内嵌）
  *_test.go    # 自动化测试
cmd/ammdemo/
  main.go      # 三组演示场景（正常序列、攻击得手、攻击被挫败）
```

## 1. 定价模型（x·y=k）

设输入侧储备 `rIn`、输出侧储备 `rOut`，用户支付含手续费的 `amountIn`，
费率 `feeBps`（30 = 0.30%）：

```
amountInNet = floor(amountIn * (10000 - feeBps) / 10000)   # 仅净额参与定价
fee         = amountIn - amountInNet
amountOut   = floor(amountInNet * rOut / (rIn + amountInNet))
```

两处 `floor` 是整数除法，输出向下取整——与 Uniswap v2 的
`getAmountOut` 完全一致。`quoteAt` 是纯函数，可在任意“假设储备”上报价，
因此无操纵公允报价、攻击损失都能用同一定价逻辑独立复算。

### 手续费口径（明确且一致）

手续费在**输入端扣除**，只有 `amountInNet` 参与换出定价；但手续费
**不离开资金池**——输入侧储备增加的是含手续费的完整金额：

```
rIn'  = rIn + amountIn
rOut' = rOut - amountOut
```

因此每笔成交后

```
k' = rIn' * rOut' >= k
```

**严格单调不降**：

- 零费率且整除时 `k' = k`（严格守恒，有测试逐笔断言）；
- 零费率但不整除时，输出向下取整会留下灰尘，`k' >= k`；
- 正费率时，留存给 LP 的手续费使 `k' > k`，增长率即 `kGrowthBps`。

由于定价完全不使用浮点，连续成千上万笔小额交易也不会出现“不变量系统性
漂移”——这是相对 float64 实现的核心区别（float64 的 0.997 连乘会累积误差）。

## 2. 价格冲击（price impact）

成交后现货价 `p = r1/r0` 相对成交前的变动幅度（基点 bps，带符号）：

```
impactBps = (pAfter/pBefore - 1) * 10000
          = (r1'*r0 / (r0'*r1) - 1) * 10000
```

它度量交易本身把曲线“推滑”的成本，与手续费相互独立；全程 `big.Rat`
有理运算，仅在最后一步转 float64 展示。有测试用独立公式与参考数值双重
核对（如新池首笔 25 WETH 卖单约 −35.96%）。

## 3. 滑点保护

`Protection` 支持两种阈值，可单独或同时使用，系统取**更严格**者：

1. **绝对最低输出** `minAmountOut`：`amountOut < minAmountOut` 即拒绝；
2. **签名储备快照 + 最大滑点百分比**：用签名时的 `(RefR0, RefR1)` 重算
   无操纵公允报价 `refOut`，阈值
   `floor(refOut * (10000 - maxSlippageBps) / 10000)`。

任一阈值不满足时：

- 返回状态 `rejected_slippage` 与原因码 `SLIPPAGE_PROTECTION`；
- 拒绝明细同时给出实际输出、公允报价、实际滑点百分比与容忍上限；
- **池状态严格不变**（储备、k 值均不变，有测试断言）。

非法输入（金额 ≤ 0、百分比保护缺少储备快照、容忍度 >100%）单独返回
`rejected_invalid / INVALID_INPUT`，同样不改变池状态。保护是可选的：
演示场景中未设保护的 Frank 会在同一劣价成交，作为对照。

## 4. 三明治攻击检测与损失口径

### 4.1 精确损益核算

- **受害者损失** = 在*攻击者建仓前储备*上计算的无攻击公允报价
  `fairOut` − 受害者实际输出。即“如果攻击者从未出手，受害者本应拿到多少”。
- **攻击者净利** = 平仓收回的建仓代币 − 建仓投入；三笔交易的手续费都已
  包含在精确报价内，因此净利是扣费后的真实值。演示场景（100 WETH /
  300,000 USDC，20 WETH 建仓夹 15 WETH 受害者）：受害者损失
  11,310.777654 USDC（约 28.98%），攻击者净利 +4.6678 WETH。
- 受害者被滑点保护拒绝时损失为 **0**；没有受害者接盘，攻击者被迫自行
  平仓，往返手续费导致净利 **−0.09995 WETH**（攻击无利可图）。

### 4.2 不依赖标签的形态启发式

`detectSandwiches` 不读取任何预设标签，仅按行为扫描三元组：

- 同一账户在 **5 分钟窗口**内发出方向**相反**的两条腿；
- 两腿各自造成至少 **10%（1000 bps）** 的价格冲击（大额交易）；
- 两腿之间夹着**另一个账户**的订单（成交或被拒绝均可——后者记为攻击未遂）。

检测后回填每笔交易的 `front/victim/back` 标记。小额正常往返、超出时间窗的
反向交易均不会误报（有对照测试）。

### 4.3 短时异常跳变

相邻两笔**已成交**交易（被拒绝的不改变价格、跳过）之间现货价变动
绝对值 ≥ 1000 bps 即记为异常跳变，输出急涨/急跌方向、触发者与间隔秒数。

## 5. 报告

`amm.WriteReport` 把结果序列化为 JSON（`big.Int` 以十进制字符串传输，
`Marshal` 默认转义 `<>&`，可安全内嵌于 `<script>`），注入 `go:embed`
的 `template.html`。报告包含：

- 每场景的现货价曲线（原生 SVG，2px 折线、形状+颜色双编码标记、
  三明治时间窗底色带、十字光标 tooltip）；
- 交易明细表（✓ 成交 / ✕ 滑点拒绝 / ✕ 非法请求、◆ 攻击腿、■ 受害者，
  支持按状态过滤）；
- 三明治损益卡片、启发式形态与异常跳变面板；
- 浅/深色双主题（跟随系统并可手动切换）、窄屏横向滚动。

文件完全自包含：没有任何 `http(s)://` 外链（SVG XML 命名空间标识符除外），
不启动服务。

## 6. 测试覆盖

```bash
go test ./...
```

| 测试 | 覆盖点 |
|---|---|
| `TestQuoteKnownVectors` / `TestQuoteFormulaIndependent` | 报价与 Uniswap v2 公式、已知输出向量一致，正/反两方向 |
| `TestInvariantZeroFeeExact` | 零费率整除序列严格保持 k 不变 |
| `TestInvariantNonDecreasing` | 2000 笔小额取整交易后 k 单调不降；正费率 k 严格增长 |
| `TestImpactMatchesTheory` | 价格冲击与独立 big.Rat 理论公式及参考值一致 |
| `TestSlippageAbsolute` / `TestSlippagePercentage` | 两种滑点保护超限即拒绝、拒绝后池状态不变、公允报价独立复算 |
| `TestInvalidInputs` / `TestQuoteDoesNotMutate` | 非法输入拒绝；报价不产生副作用 |
| `TestSandwichVictimLoss` / `TestSandwichAttackerNet` | 受害者损失与攻击者净利（含两腿手续费）计算正确 |
| `TestSandwichBlocked` | 保护挫败攻击：零受害者损失、攻击者净亏、仍被识别为未遂 |
| `TestHeuristicNoLabels` | 启发式不依赖标签；小额/超窗不误报 |
| `TestJumpDetection` | 急涨急跌跳变计数与方向；被拒绝步骤不产生跳变 |
| `TestSandwichRoundTripFeeConsistency` | 攻击往返全程 k 单调不降 |
| `TestWriteReportSelfContained` | 占位符被替换、JSON 合法、无外部网络引用 |

## 7. 局限与边界

- 聚焦核心 x·y=k 模型，未建模稳定币对、多跳路由、LP 铸烧、闪电贷与
  EVM 内存池竞争；检测阈值（10% 跳变、5 分钟窗）为监控启发式，可按
  场景在 `detect.go` 常量中调整。
- 人类可读金额与价格使用 float64 仅作展示，所有决策与审计数值以原始
  整数（表格行 title 与 JSON 的 `*Raw` 字段）为准。
