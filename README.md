# amm-slippage

恒定乘积做市商（Constant Product AMM,Uniswap v2 核心模型）的交易执行、滑点保护与三明治攻击检测。仅使用 Go 标准库;报告为单一静态 HTML 文件,仅原生 HTML/CSS/JavaScript,无任何第三方库。

## 运行

```bash
go test ./...          # 运行全部测试
go run ./cmd/ammviz    # 生成 report.html(可用 -o 指定输出路径)
open report.html       # 浏览器打开报告
```

## AMM 定价模型

资金池持有两种代币储备 `x`(X)与 `y`(Y),满足不变量 `x · y = k`。卖出 `Δx` 买入 Y 时,成交后必须满足 `(x + Δx)(y − Δy) ≥ k`,由此得到输出公式。

**价格冲击(price impact)**:中间价(边际价格)为 `y/x`,而大额交易的有效执行价为 `Δy/Δx < y/x`。本实现报告

```
priceImpact = 1 − execPrice / midPriceBefore
```

零手续费时理论值为 `Δx / (x + Δx)`(测试 `TestPriceImpactMatchesTheory` 验证一致性,允许一个最小取整单位的误差)。

## 手续费处理方式

采用 Uniswap v2 约定:输入金额的 0.3%(`FeeNum=997, FeeDen=1000`)作为手续费**留存在池内**,不单独提取:

```
amountInWithFee = Δx · 997
Δy = amountInWithFee · y / (x · 1000 + amountInWithFee)   (整数向下取整)
```

因为手续费留在储备中,每笔非零交易后 `k′ = (x+Δx)(y−Δy) > k`,即 **k 单调递增**,增量归流动性提供者。全部计算使用 `math/big` 整数,除法向下取整且永远有利于资金池,因此**不存在浮点误差累积**:连续任意多次小额交易后不变量不会系统性偏离(测试 `TestInvariantMonotoneOverManySwaps` 以 500 笔交易验证 k 严格递增;`TestInvariantExactWithZeroFee` 验证零手续费时 k 从不下降)。

## 滑点保护

`Pool.Swap(xToY, amountIn, minAmountOut)` 在执行前计算输出,若 `amountOut < minAmountOut` 则返回 `ErrSlippageExceeded` 并**完全不改池状态**,拒绝原因会记录进交易日志。辅助函数 `MinOutFromBps(quote, bps)` 把"最大滑点百分比"换算为 `minAmountOut`:

```
minAmountOut = quote · (10000 − bps) / 10000
```

典型流程:用户按当前池状态报价并设定容忍度(如 0.5%),若交易上链前价格被其他交易(如鲸鱼单或三明治前置)推动,实际输出低于阈值即拒绝,而不是静默成交一个滑点超预期的交易。

## 三明治攻击检测

`DetectSandwiches(pool, trades, window)` 在执行序列上匹配模式:

1. **前置**:攻击者 A 以方向 d 交易(推高受害者买入方向的价格);
2. **受害者**:另一账户以相同方向 d 交易;
3. **平仓**:A 在 window 笔交易内以反方向交易,且前后两笔之间 A 无其他交易。

对每个三元组计算:

- **受害者损失**:在移除攻击者两笔交易的基线序列中重放,受害者本可获得的输出 − 实际输出;
- **攻击者利润**:平仓输出 − 前置输入(以前置输入代币计价)。

无利可图的往返(普通用户的双向交易)不计为攻击。另外,`ExecuteSequence` 会对中间价跳变超过阈值(报告默认 1%)的交易打 `Anomaly` 标记,用于识别短时间内的异常价格跳变。

## 报告

`cmd/ammviz` 构造包含正常交易、鲸鱼大单、滑点保护拒绝与一次完整三明治攻击的示例序列,输出单一静态 HTML:价格曲线(SVG)随交易序号变化,高亮被拒绝交易(红)、攻击者前置/平仓(橙)、受害者(紫)与异常跳变(黄圈),并附交易明细表与 k 值增长摘要。数据与逻辑全部内嵌,双击即可在浏览器打开。

## 项目结构

```
amm/pool.go          恒定乘积池、报价、带滑点保护的交换(math/big 整数运算)
amm/sandwich.go      交易序列执行日志、三明治攻击检测与损失计算
amm/pool_test.go     不变量、手续费、滑点拒绝、价格冲击理论值测试
amm/sandwich_test.go 三明治场景受害者损失/攻击者利润、序列拒绝测试
cmd/ammviz/main.go   示例场景 + 静态 HTML 报告生成
```
