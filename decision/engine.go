package decision

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"nofx/market"
	"nofx/mcp"
	"nofx/pool"
	"strings"
	"time"
)

// PositionInfo 持仓信息
type PositionInfo struct {
	Symbol           string  `json:"symbol"`
	Side             string  `json:"side"`
	EntryPrice       float64 `json:"entry_price"`
	MarkPrice        float64 `json:"mark_price"`
	Quantity         float64 `json:"quantity"`
	Leverage         int     `json:"leverage"`
	UnrealizedPnL    float64 `json:"unrealized_pnl"`
	UnrealizedPnLPct float64 `json:"unrealized_pnl_pct"`
	LiquidationPrice float64 `json:"liquidation_price"`
	MarginUsed       float64 `json:"margin_used"`
	UpdateTime       int64   `json:"update_time"`
	StopLoss         float64 `json:"stop_loss,omitempty"`   // 新增：当前止损价
	TakeProfit       float64 `json:"take_profit,omitempty"` // 新增：当前止盈价
}

// AccountInfo 账户信息
type AccountInfo struct {
	TotalEquity      float64 `json:"total_equity"`
	AvailableBalance float64 `json:"available_balance"`
	TotalPnL         float64 `json:"total_pnl"`
	TotalPnLPct      float64 `json:"total_pnl_pct"`
	MarginUsed       float64 `json:"margin_used"`
	MarginUsedPct    float64 `json:"margin_used_pct"`
	PositionCount    int     `json:"position_count"`
}

// CandidateCoin 候选币种
type CandidateCoin struct {
	Symbol  string   `json:"symbol"`
	Sources []string `json:"sources"`
}

// OITopData 持仓量增长Top数据
type OITopData struct {
	Rank              int
	OIDeltaPercent    float64
	OIDeltaValue      float64
	PriceDeltaPercent float64
	NetLong           float64
	NetShort          float64
}

// CorrelationData 相关性数据（新增）
type CorrelationData struct {
	Symbol     string  `json:"symbol"`
	BTCCorr    float64 `json:"btc_correlation"` // 与BTC的相关系数
	IsHighCorr bool    `json:"is_high_corr"`    // 是否高相关（>0.8）
	RiskWeight float64 `json:"risk_weight"`     // 风险权重
}

// CircuitBreakerState 熔断状态（新增）
type CircuitBreakerState struct {
	IsTriggered     bool      `json:"is_triggered"`
	TriggerReason   string    `json:"trigger_reason"`
	TriggerTime     time.Time `json:"trigger_time"`
	CooldownMinutes int       `json:"cooldown_minutes"`
}

// Context 交易上下文（优化版）
type Context struct {
	CurrentTime     string                      `json:"current_time"`
	RuntimeMinutes  int                         `json:"runtime_minutes"`
	CallCount       int                         `json:"call_count"`
	Account         AccountInfo                 `json:"account"`
	Positions       []PositionInfo              `json:"positions"`
	CandidateCoins  []CandidateCoin             `json:"candidate_coins"`
	MarketDataMap   map[string]*market.Data     `json:"-"`
	OITopDataMap    map[string]*OITopData       `json:"-"`
	CorrelationMap  map[string]*CorrelationData `json:"-"` // 新增：相关性数据
	CircuitBreaker  *CircuitBreakerState        `json:"-"` // 新增：熔断状态
	Performance     interface{}                 `json:"-"`
	BTCETHLeverage  int                         `json:"-"`
	AltcoinLeverage int                         `json:"-"`
	MaxRiskPerTrade float64                     `json:"-"` // 新增：单笔最大风险比例
	TotalRiskBudget float64                     `json:"-"` // 新增：总风险预算
}

// Decision AI的交易决策
type Decision struct {
	Symbol          string  `json:"symbol"`
	Action          string  `json:"action"`
	Leverage        int     `json:"leverage,omitempty"`
	PositionSizeUSD float64 `json:"position_size_usd,omitempty"`
	StopLoss        float64 `json:"stop_loss,omitempty"`
	TakeProfit      float64 `json:"take_profit,omitempty"`
	NewStopLoss     float64 `json:"new_stop_loss,omitempty"`
	NewTakeProfit   float64 `json:"new_take_profit,omitempty"`
	ClosePercentage float64 `json:"close_percentage,omitempty"`
	Confidence      int     `json:"confidence,omitempty"`
	RiskUSD         float64 `json:"risk_usd,omitempty"`
	Reasoning       string  `json:"reasoning"`
}

// FullDecision AI的完整决策
type FullDecision struct {
	UserPrompt string     `json:"user_prompt"`
	CoTTrace   string     `json:"cot_trace"`
	Decisions  []Decision `json:"decisions"`
	Timestamp  time.Time  `json:"timestamp"`
}

// GetFullDecision 获取AI的完整交易决策（优化版）
func GetFullDecision(ctx *Context, mcpClient *mcp.Client) (*FullDecision, error) {
	// 0. 检查熔断状态
	if ctx.CircuitBreaker != nil && ctx.CircuitBreaker.IsTriggered {
		cooldownEnd := ctx.CircuitBreaker.TriggerTime.Add(time.Duration(ctx.CircuitBreaker.CooldownMinutes) * time.Minute)
		if time.Now().Before(cooldownEnd) {
			remainingMinutes := int(cooldownEnd.Sub(time.Now()).Minutes())
			return &FullDecision{
				CoTTrace: fmt.Sprintf("⚠️ 熔断中: %s | 剩余冷却时间: %d分钟",
					ctx.CircuitBreaker.TriggerReason, remainingMinutes),
				Decisions: []Decision{{
					Symbol:    "ALL",
					Action:    "wait",
					Reasoning: fmt.Sprintf("熔断保护触发: %s", ctx.CircuitBreaker.TriggerReason),
				}},
				Timestamp: time.Now(),
			}, nil
		}
		// 熔断结束，重置状态
		ctx.CircuitBreaker.IsTriggered = false
	}

	// 1. 初始化风险参数
	if ctx.MaxRiskPerTrade == 0 {
		ctx.MaxRiskPerTrade = 0.03 // 默认单笔风险3%
	}
	if ctx.TotalRiskBudget == 0 {
		ctx.TotalRiskBudget = 0.08 // 默认总风险预算8%
	}

	// 2. 获取市场数据
	if err := fetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("获取市场数据失败: %w", err)
	}

	// 3. 检查熔断条件
	if shouldTriggerCircuitBreaker(ctx) {
		return &FullDecision{
			CoTTrace: "🛑 触发熔断保护，暂停交易",
			Decisions: []Decision{{
				Symbol:    "ALL",
				Action:    "wait",
				Reasoning: ctx.CircuitBreaker.TriggerReason,
			}},
			Timestamp: time.Now(),
		}, nil
	}

	// 4. 计算相关性矩阵
	calculateCorrelationMatrix(ctx)

	// 5. 构建prompts
	systemPrompt := buildSystemPromptEnhanced(ctx)
	userPrompt := buildUserPromptEnhanced(ctx)

	// 6. 调用AI API
	aiResponse, err := mcpClient.CallWithMessages(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("调用AI API失败: %w", err)
	}

	// 7. 解析响应
	decision, err := parseFullDecisionResponse(aiResponse, ctx)
	if err != nil {
		return nil, fmt.Errorf("解析AI响应失败: %w", err)
	}

	decision.Timestamp = time.Now()
	decision.UserPrompt = userPrompt
	return decision, nil
}

// shouldTriggerCircuitBreaker 检查是否应该触发熔断（新增）
func shouldTriggerCircuitBreaker(ctx *Context) bool {
	if ctx.CircuitBreaker == nil {
		ctx.CircuitBreaker = &CircuitBreakerState{}
	}

	// 条件1: BTC 1小时跌幅 > 5%
	if btcData, ok := ctx.MarketDataMap["BTCUSDT"]; ok {
		if btcData.PriceChange1h < -5.0 {
			ctx.CircuitBreaker.IsTriggered = true
			ctx.CircuitBreaker.TriggerReason = fmt.Sprintf("BTC 1小时暴跌 %.2f%%", btcData.PriceChange1h)
			ctx.CircuitBreaker.TriggerTime = time.Now()
			ctx.CircuitBreaker.CooldownMinutes = 30
			log.Printf("🛑 熔断触发: %s", ctx.CircuitBreaker.TriggerReason)
			return true
		}
	}

	// 条件2: 账户日内回撤 > 10%
	if ctx.Account.TotalPnLPct < -10.0 {
		ctx.CircuitBreaker.IsTriggered = true
		ctx.CircuitBreaker.TriggerReason = fmt.Sprintf("账户回撤 %.2f%% 超过10%%", ctx.Account.TotalPnLPct)
		ctx.CircuitBreaker.TriggerTime = time.Now()
		ctx.CircuitBreaker.CooldownMinutes = 60
		log.Printf("🛑 熔断触发: %s", ctx.CircuitBreaker.TriggerReason)
		return true
	}

	// 条件3: 保证金使用率 > 95%
	if ctx.Account.MarginUsedPct > 95.0 {
		ctx.CircuitBreaker.IsTriggered = true
		ctx.CircuitBreaker.TriggerReason = fmt.Sprintf("保证金使用率 %.2f%% 过高", ctx.Account.MarginUsedPct)
		ctx.CircuitBreaker.TriggerTime = time.Now()
		ctx.CircuitBreaker.CooldownMinutes = 15
		log.Printf("🛑 熔断触发: %s", ctx.CircuitBreaker.TriggerReason)
		return true
	}

	return false
}

// calculateCorrelationMatrix 计算相关性矩阵（新增）
func calculateCorrelationMatrix(ctx *Context) {
	ctx.CorrelationMap = make(map[string]*CorrelationData)

	// 获取BTC价格序列
	btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]
	if !hasBTC || btcData.MidTermSeries1h == nil {
		return
	}
	btcPrices := btcData.MidTermSeries1h.MidPrices

	for symbol, data := range ctx.MarketDataMap {
		if symbol == "BTCUSDT" {
			ctx.CorrelationMap[symbol] = &CorrelationData{
				Symbol:     symbol,
				BTCCorr:    1.0,
				IsHighCorr: true,
				RiskWeight: 1.0,
			}
			continue
		}

		if data.MidTermSeries1h == nil {
			continue
		}

		prices := data.MidTermSeries1h.MidPrices
		corr := market.CalculateCorrelation(btcPrices, prices)

		isHighCorr := math.Abs(corr) > 0.8
		riskWeight := 1.0
		if isHighCorr {
			riskWeight = 0.7 // 高相关资产风险权重降低
		} else if math.Abs(corr) < 0.5 {
			riskWeight = 1.0 // 低相关资产可以独立计算
		} else {
			riskWeight = 0.85
		}

		ctx.CorrelationMap[symbol] = &CorrelationData{
			Symbol:     symbol,
			BTCCorr:    corr,
			IsHighCorr: isHighCorr,
			RiskWeight: riskWeight,
		}
	}
}

// fetchMarketDataForContext 获取市场数据（优化版）
func fetchMarketDataForContext(ctx *Context) error {
	ctx.MarketDataMap = make(map[string]*market.Data)
	ctx.OITopDataMap = make(map[string]*OITopData)

	symbolSet := make(map[string]bool)

	// 必须包含BTC
	symbolSet["BTCUSDT"] = true

	// 持仓币种
	for _, pos := range ctx.Positions {
		symbolSet[pos.Symbol] = true
	}

	// 候选币种
	maxCandidates := calculateMaxCandidates(ctx)
	for i, coin := range ctx.CandidateCoins {
		if i >= maxCandidates {
			break
		}
		symbolSet[coin.Symbol] = true
	}

	// 持仓币种集合
	positionSymbols := make(map[string]bool)
	for _, pos := range ctx.Positions {
		positionSymbols[pos.Symbol] = true
	}

	for symbol := range symbolSet {
		data, err := market.Get(symbol)
		if err != nil {
			log.Printf("⚠️ 获取 %s 数据失败: %v", symbol, err)
			continue
		}

		// 流动性过滤（持仓除外）
		isExistingPosition := positionSymbols[symbol]
		if !isExistingPosition && data.OIValueUSD > 0 {
			oiValueInMillions := data.OIValueUSD / 1_000_000
			if oiValueInMillions < 15 {
				log.Printf("⚠️ %s OI价值过低(%.2fM USD < 15M)，跳过", symbol, oiValueInMillions)
				continue
			}
		}

		ctx.MarketDataMap[symbol] = data
	}

	// 加载OI Top数据
	oiPositions, err := pool.GetOITopPositions()
	if err == nil {
		for _, pos := range oiPositions {
			ctx.OITopDataMap[pos.Symbol] = &OITopData{
				Rank:              pos.Rank,
				OIDeltaPercent:    pos.OIDeltaPercent,
				OIDeltaValue:      pos.OIDeltaValue,
				PriceDeltaPercent: pos.PriceDeltaPercent,
				NetLong:           pos.NetLong,
				NetShort:          pos.NetShort,
			}
		}
	}

	return nil
}

// calculateMaxCandidates 计算候选币种数量
func calculateMaxCandidates(ctx *Context) int {
	return len(ctx.CandidateCoins)
}

// buildSystemPromptEnhanced 构建增强版System Prompt
func buildSystemPromptEnhanced(ctx *Context) string {
	var sb strings.Builder

	availableBalance := ctx.Account.AvailableBalance
	btcEthLeverage := ctx.BTCETHLeverage
	altcoinLeverage := ctx.AltcoinLeverage

	// === 核心使命 ===
	sb.WriteString("你是专业的加密货币交易AI，核心目标是**最大化夏普比率**。\n\n")

	// === 核心目标（简化版）===
	sb.WriteString("# 🎯 核心目标: 最大化夏普比率\n\n")
	sb.WriteString("夏普比率 = (平均收益 - 无风险收益) / 收益波动率\n\n")
	sb.WriteString("**高质量交易** = 高胜率 + 大盈亏比 + 低相关性 → 提升夏普\n")
	sb.WriteString("**量化标准**: 每天2-4笔，每小时0.1-0.2笔。>2笔/小时 = 过度交易\n\n")

	// === 硬约束（风险控制）===
	maxPositionForAltcoin := availableBalance * float64(altcoinLeverage) * 0.9
	maxPositionForBTCETH := availableBalance * float64(btcEthLeverage) * 0.9

	sb.WriteString("# ⚖️ 硬约束\n\n")
	sb.WriteString("1. **风险回报比**: ≥ 1:3\n")
	sb.WriteString("2. **最多持仓**: 3个币种\n")
	sb.WriteString(fmt.Sprintf("3. **仓位上限**: 山寨币 %.0f USD | BTC/ETH %.0f USD\n", maxPositionForAltcoin, maxPositionForBTCETH))
	sb.WriteString("4. **保证金使用率**: ≤ 90%\n")
	sb.WriteString("5. **OI价值**: ≥ 15M USD\n")
	sb.WriteString("6. **单笔风险**: ≤ 账户净值的2%\n")
	sb.WriteString("7. **总风险预算**: ≤ 账户净值的5%\n\n")

	// === 波动率自适应仓位（新增核心）===
	sb.WriteString("# 💵 波动率自适应仓位计算（核心优化）\n\n")
	sb.WriteString("**公式**:\n")
	sb.WriteString("```\n")
	sb.WriteString("止损距离 = ATR14 × 倍数（山寨3.5，BTC/ETH 2.5）\n")
	sb.WriteString("仓位大小 = (账户净值 × 单笔风险比例) / 止损百分比\n")
	sb.WriteString("```\n")
	sb.WriteString("**示例**: 账户1000 USDT，ATR14=50，当前价=1000，风险2%\n")
	sb.WriteString("  - 止损距离 = 50 × 2.5 = 125 USDT\n")
	sb.WriteString("  - 止损百分比 = 125/1000 = 12.5%\n")
	sb.WriteString("  - 仓位大小 = (1000 × 2%) / 12.5% = 160 USDT\n\n")

	// === 相关性控制（新增）===
	sb.WriteString("# 📐 持仓相关性控制（新增）\n\n")
	sb.WriteString("**规则**:\n")
	sb.WriteString("  - 高相关（ρ > 0.8）：视为同一风险敞口，仓位×0.7\n")
	sb.WriteString("  - 中相关（0.5 < ρ < 0.8）：仓位×0.85\n")
	sb.WriteString("  - 低相关（ρ < 0.5）：可独立计算\n")
	sb.WriteString("  - **同方向高相关持仓不超过2个**\n\n")

	// === ADX市场状态识别（新增）===
	sb.WriteString("# 🌊 市场状态量化识别（ADX）\n\n")
	sb.WriteString("**ADX趋势强度分级**:\n")
	sb.WriteString("  - ADX > 25 + DI+ > DI-：**强上升趋势** → 做多为主\n")
	sb.WriteString("  - ADX > 25 + DI- > DI+：**强下降趋势** → 做空为主\n")
	sb.WriteString("  - ADX 20-25：**趋势形成中** → 等待确认\n")
	sb.WriteString("  - ADX < 20：**震荡市场** → 观望或快进快出\n")
	sb.WriteString("  - 布林带宽度 < 2%：**波动收缩** → 即将突破\n\n")

	// === 多时间框架加权评分（新增）===
	sb.WriteString("# 🎚️ 多时间框架加权评分\n\n")
	sb.WriteString("**信心度计算**:\n")
	sb.WriteString("```\n")
	sb.WriteString("Confidence = 0.35×S_4h + 0.30×S_1h + 0.20×S_15m + 0.15×S_3m\n")
	sb.WriteString("```\n")
	sb.WriteString("**各框架评分** (0-100):\n")
	sb.WriteString("  - 趋势方向一致：+30分\n")
	sb.WriteString("  - MACD支持：+25分\n")
	sb.WriteString("  - RSI合理区间：+20分\n")
	sb.WriteString("  - 量价配合：+15分\n")
	sb.WriteString("  - OI支持：+10分\n\n")

	// === 资金费率分级（优化）===
	sb.WriteString("# 💰 资金费率分级策略\n\n")
	sb.WriteString("| 费率范围 | 信号 | 操作建议 |\n")
	sb.WriteString("|---------|------|----------|\n")
	sb.WriteString("| > 0.1% | 极端看多 | 强烈做空信号 + 年化109%收益 |\n")
	sb.WriteString("| 0.05-0.1% | 偏多 | 做空优先级提升 |\n")
	sb.WriteString("| 0.03-0.05% | 轻度偏多 | 关注其他指标 |\n")
	sb.WriteString("| -0.03-0.03% | 中性 | 技术面主导 |\n")
	sb.WriteString("| < -0.05% | 偏空 | 做多优先级提升 |\n")
	sb.WriteString("| < -0.1% | 极端看空 | 强烈做多信号 |\n\n")

	// === 交易成本计算（新增）===
	sb.WriteString("# 💸 真实风险回报比（扣除成本）\n\n")
	sb.WriteString("**成本构成**:\n")
	sb.WriteString("  - Maker: 0.02% | Taker: 0.05%\n")
	sb.WriteString("  - 滑点: 0.03-0.1%（根据OI）\n")
	sb.WriteString("  - **往返成本**: 约0.16-0.3%\n\n")
	sb.WriteString("**原始RR ≥ 1:3.5** 才能保证真实RR ≥ 1:3\n\n")

	// === 熔断机制（新增）===
	sb.WriteString("# 🛡️ 熔断保护机制\n\n")
	sb.WriteString("**自动触发条件**:\n")
	sb.WriteString("  - BTC 1小时跌幅 > 5%：冷却30分钟\n")
	sb.WriteString("  - 账户日内回撤 > 10%：冷却60分钟\n")
	sb.WriteString("  - 保证金使用率 > 95%：冷却15分钟\n\n")

	// === 移动止损规则 ===
	sb.WriteString("# 📊 移动止损规则\n\n")
	sb.WriteString("| 盈利阈值 | 止损调整 |\n")
	sb.WriteString("|---------|----------|\n")
	sb.WriteString("| ≥7% | 移至入场价（保本）|\n")
	sb.WriteString("| ≥10% | 移至盈利2%位置 |\n")
	sb.WriteString("| ≥15% | 移至盈利5%位置 |\n\n")

	// === 分批止盈规则 ===
	sb.WriteString("# 🎯 分批止盈规则\n\n")
	sb.WriteString("| 达到目标 | 操作 |\n")
	sb.WriteString("|---------|------|\n")
	sb.WriteString("| RR 1:3 | 平仓50%，剩余持有 |\n")
	sb.WriteString("| RR 1:5 | 再平30%，剩余20%博取更大收益 |\n\n")

	// === 决策流程 ===
	sb.WriteString("# 📋 决策流程\n\n")
	sb.WriteString("1. **检查熔断状态** → 触发则wait\n")
	sb.WriteString("2. **分析夏普比率** → 调整策略\n")
	sb.WriteString("3. **评估BTC趋势（ADX+EMA）** → 确定大方向\n")
	sb.WriteString("4. **检查相关性** → 避免风险集中\n")
	sb.WriteString("5. **评估持仓** → hold/close/update_stop_loss\n")
	sb.WriteString("6. **寻找新机会** → 多维度确认\n")
	sb.WriteString("7. **计算波动率自适应仓位** → ATR-based\n")
	sb.WriteString("8. **输出决策**\n\n")

	// === 输出格式 ===
	sb.WriteString("# 📤 输出格式\n\n")
	sb.WriteString("**第一步**: 思维链（纯文本分析）\n")
	sb.WriteString("**第二步**: JSON决策数组\n\n")

	sb.WriteString("**可用动作**:\n")
	sb.WriteString("1. `open_long` / `open_short`: 开仓\n")
	sb.WriteString("2. `close_long` / `close_short`: 平仓\n")
	sb.WriteString("3. `update_stop_loss`: 调整止损\n")
	sb.WriteString("4. `update_take_profit`: 调整止盈\n")
	sb.WriteString("5. `partial_close`: 部分平仓\n")
	sb.WriteString("6. `hold`: 持有\n")
	sb.WriteString("7. `wait`: 观望\n\n")

	sb.WriteString("**JSON示例**:\n")
	sb.WriteString("```json\n[\n")
	sb.WriteString(fmt.Sprintf("  {\"symbol\": \"BTCUSDT\", \"action\": \"open_short\", \"leverage\": %d, \"position_size_usd\": %.0f, \"stop_loss\": 97000, \"take_profit\": 91000, \"confidence\": 85, \"risk_usd\": %.0f, \"reasoning\": \"ADX=32(强下降趋势)+MACD死叉+资金费率0.08%%+风险回报比1:4\"},\n",
		btcEthLeverage, availableBalance*3, availableBalance*0.02))
	sb.WriteString("  {\"symbol\": \"SOLUSDT\", \"action\": \"wait\", \"reasoning\": \"BTC弱势,与BTC相关性0.85(高),等待独立信号\"}\n")
	sb.WriteString("]\n```\n\n")

	// === 核心原则 ===
	sb.WriteString("---\n**核心原则**: 质量>数量 | 做空=做多 | 风险回报比≥1:3 | BTC是龙头 | 让利润奔跑\n")

	return sb.String()
}

// buildUserPromptEnhanced 构建增强版User Prompt
func buildUserPromptEnhanced(ctx *Context) string {
	var sb strings.Builder

	// 系统状态
	sb.WriteString(fmt.Sprintf("**时间**: %s | **周期**: #%d | **运行**: %d分钟\n\n",
		ctx.CurrentTime, ctx.CallCount, ctx.RuntimeMinutes))

	// BTC 市场状态（增强版）
	if btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]; hasBTC {
		marketState, stateConfidence := market.GetMarketState(btcData)
		sb.WriteString(fmt.Sprintf("## 🪙 BTC状态\n"))
		sb.WriteString(fmt.Sprintf("**价格**: %.2f | **1h**: %+.2f%% | **4h**: %+.2f%%\n",
			btcData.CurrentPrice, btcData.PriceChange1h, btcData.PriceChange4h))
		sb.WriteString(fmt.Sprintf("**ADX**: %.1f | **DI+**: %.1f | **DI-**: %.1f | **市场状态**: **%s** (置信度%d%%)\n",
			btcData.CurrentADX, btcData.CurrentDIPlus, btcData.CurrentDIMinus, marketState, stateConfidence))
		sb.WriteString(fmt.Sprintf("**MACD**: %.4f | **RSI14**: %.1f | **资金费率**: %.4f%%\n\n",
			btcData.CurrentMACD, btcData.CurrentRSI14, btcData.FundingRate*100))
	}

	// 账户
	sb.WriteString(fmt.Sprintf("## 💰 账户状态\n"))
	sb.WriteString(fmt.Sprintf("**净值**: %.2f | **可用**: %.2f (%.1f%%) | **盈亏**: %+.2f%% | **保证金**: %.1f%% | **持仓**: %d个\n\n",
		ctx.Account.TotalEquity,
		ctx.Account.AvailableBalance,
		(ctx.Account.AvailableBalance/ctx.Account.TotalEquity)*100,
		ctx.Account.TotalPnLPct,
		ctx.Account.MarginUsedPct,
		ctx.Account.PositionCount))

	// 风险预算
	usedRisk := calculateUsedRisk(ctx)
	remainingRisk := ctx.TotalRiskBudget - usedRisk
	sb.WriteString(fmt.Sprintf("**风险预算**: 已用 %.1f%% / 总额 %.1f%% | **剩余**: %.1f%%\n\n",
		usedRisk*100, ctx.TotalRiskBudget*100, remainingRisk*100))

	// 持仓（带相关性和市场状态）
	if len(ctx.Positions) > 0 {
		sb.WriteString("## 📊 当前持仓\n")
		for i, pos := range ctx.Positions {
			// 持仓时长
			holdingDuration := ""
			if pos.UpdateTime > 0 {
				durationMs := time.Now().UnixMilli() - pos.UpdateTime
				durationMin := durationMs / (1000 * 60)
				if durationMin < 60 {
					holdingDuration = fmt.Sprintf("%d分钟", durationMin)
				} else {
					holdingDuration = fmt.Sprintf("%d小时%d分钟", durationMin/60, durationMin%60)
				}
			}

			// 相关性
			corrInfo := ""
			if corr, ok := ctx.CorrelationMap[pos.Symbol]; ok {
				corrInfo = fmt.Sprintf("BTC相关性: %.2f", corr.BTCCorr)
				if corr.IsHighCorr {
					corrInfo += " (高)"
				}
			}

			sb.WriteString(fmt.Sprintf("### %d. %s %s\n", i+1, pos.Symbol, strings.ToUpper(pos.Side)))
			sb.WriteString(fmt.Sprintf("**入场**: %.4f | **当前**: %.4f | **盈亏**: %+.2f%% | **杠杆**: %dx | **时长**: %s\n",
				pos.EntryPrice, pos.MarkPrice, pos.UnrealizedPnLPct, pos.Leverage, holdingDuration))
			sb.WriteString(fmt.Sprintf("**保证金**: %.0f | **强平价**: %.4f | %s\n\n",
				pos.MarginUsed, pos.LiquidationPrice, corrInfo))

			// 移动止损建议
			if pos.UnrealizedPnLPct >= 15 {
				newSL := pos.EntryPrice * 1.05 // 锁定5%盈利
				if pos.Side == "short" {
					newSL = pos.EntryPrice * 0.95
				}
				sb.WriteString(fmt.Sprintf("⚠️ **建议移动止损至 %.4f** (锁定5%%盈利)\n\n", newSL))
			} else if pos.UnrealizedPnLPct >= 10 {
				newSL := pos.EntryPrice * 1.02
				if pos.Side == "short" {
					newSL = pos.EntryPrice * 0.98
				}
				sb.WriteString(fmt.Sprintf("⚠️ **建议移动止损至 %.4f** (锁定2%%盈利)\n\n", newSL))
			} else if pos.UnrealizedPnLPct >= 7 {
				sb.WriteString(fmt.Sprintf("⚠️ **建议移动止损至入场价 %.4f** (保本)\n\n", pos.EntryPrice))
			}

			// 市场数据
			if marketData, ok := ctx.MarketDataMap[pos.Symbol]; ok {
				sb.WriteString(market.Format(marketData))
			}
		}
	} else {
		sb.WriteString("## 📊 当前持仓: 无\n\n")
	}

	// 候选币种（带相关性）
	sb.WriteString(fmt.Sprintf("## 🔍 候选币种 (%d个)\n\n", len(ctx.MarketDataMap)-1)) // 减去BTC
	displayedCount := 0
	for _, coin := range ctx.CandidateCoins {
		if coin.Symbol == "BTCUSDT" {
			continue
		}
		marketData, hasData := ctx.MarketDataMap[coin.Symbol]
		if !hasData {
			continue
		}
		displayedCount++

		// 来源标签
		sourceTags := ""
		if len(coin.Sources) > 1 {
			sourceTags = " (AI500+OI_Top)"
		} else if len(coin.Sources) == 1 && coin.Sources[0] == "oi_top" {
			sourceTags = " (OI_Top)"
		}

		// 相关性信息
		corrInfo := ""
		if corr, ok := ctx.CorrelationMap[coin.Symbol]; ok {
			corrInfo = fmt.Sprintf(" | BTC相关性: %.2f", corr.BTCCorr)
			if corr.IsHighCorr {
				corrInfo += " (高风险权重×0.7)"
			}
		}

		// 市场状态
		marketState, _ := market.GetMarketState(marketData)

		// 波动率自适应仓位建议
		isAltcoin := coin.Symbol != "BTCUSDT" && coin.Symbol != "ETHUSDT"
		suggestedSize, stopDist := market.CalculateAdaptivePositionSize(
			ctx.Account.TotalEquity,
			marketData.LongerTermContext.ATR14,
			marketData.CurrentPrice,
			ctx.MaxRiskPerTrade,
			isAltcoin,
		)

		sb.WriteString(fmt.Sprintf("### %d. %s%s\n", displayedCount, coin.Symbol, sourceTags))
		sb.WriteString(fmt.Sprintf("**状态**: %s%s\n", marketState, corrInfo))
		sb.WriteString(fmt.Sprintf("**建议仓位**: %.0f USD (基于ATR=%.4f, 止损距离=%.4f)\n\n",
			suggestedSize, marketData.LongerTermContext.ATR14, stopDist))

		// 完整市场数据
		sb.WriteString(market.Format(marketData))
		sb.WriteString("\n")
	}

	// 夏普比率
	if ctx.Performance != nil {
		type PerformanceData struct {
			SharpeRatio    float64 `json:"sharpe_ratio"`
			WinRate        float64 `json:"win_rate"`
			AvgHoldingTime float64 `json:"avg_holding_time"`
			TradesPerHour  float64 `json:"trades_per_hour"`
		}
		var perfData PerformanceData
		if jsonData, err := json.Marshal(ctx.Performance); err == nil {
			if err := json.Unmarshal(jsonData, &perfData); err == nil {
				sb.WriteString(fmt.Sprintf("## 📈 绩效指标\n"))
				sb.WriteString(fmt.Sprintf("**夏普比率**: %.2f | **胜率**: %.1f%% | **平均持仓**: %.0f分钟 | **交易频率**: %.2f笔/小时\n\n",
					perfData.SharpeRatio, perfData.WinRate*100, perfData.AvgHoldingTime, perfData.TradesPerHour))

				// 策略建议
				if perfData.SharpeRatio < -0.5 {
					sb.WriteString("⚠️ **夏普<-0.5**: 停止交易，观望至少6个周期，提高开仓门槛至85\n\n")
				} else if perfData.SharpeRatio < 0 {
					sb.WriteString("⚠️ **夏普<0**: 严格控制，只做信心度>80的交易\n\n")
				} else if perfData.SharpeRatio > 0.7 {
					sb.WriteString("✅ **夏普>0.7**: 可适度扩大仓位\n\n")
				}
			}
		}
	}

	sb.WriteString("---\n")
	sb.WriteString("请分析并输出决策（思维链 + JSON）\n")

	return sb.String()
}

// calculateUsedRisk 计算已使用的风险（新增）
func calculateUsedRisk(ctx *Context) float64 {
	if ctx.Account.TotalEquity <= 0 {
		return 0
	}

	totalRisk := 0.0
	for _, pos := range ctx.Positions {
		// 简化计算：使用保证金占比作为风险估算
		posRisk := pos.MarginUsed / ctx.Account.TotalEquity

		// 应用相关性风险权重
		// 注意: 相关性风险权重已在仓位验证时应用，此处不再重复计算        
		// 避免双重惩罚导致风险预算计算失真
		//if corr, ok := ctx.CorrelationMap[pos.Symbol]; ok {
		//	posRisk *= corr.RiskWeight
		//}

		totalRisk += posRisk
	}

	return totalRisk
}

// parseFullDecisionResponse 解析AI响应（优化版）
func parseFullDecisionResponse(aiResponse string, ctx *Context) (*FullDecision, error) {
	cotTrace := extractCoTTrace(aiResponse)
	decisions, err := extractDecisions(aiResponse)
	if err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: []Decision{},
		}, fmt.Errorf("提取决策失败: %w\n\n=== AI思维链 ===\n%s", err, cotTrace)
	}

	// 验证决策
	if err := validateDecisionsEnhanced(decisions, ctx); err != nil {
		return &FullDecision{
			CoTTrace:  cotTrace,
			Decisions: decisions,
		}, fmt.Errorf("决策验证失败: %w\n\n=== AI思维链 ===\n%s", err, cotTrace)
	}

	return &FullDecision{
		CoTTrace:  cotTrace,
		Decisions: decisions,
	}, nil
}

// extractCoTTrace 提取思维链
func extractCoTTrace(response string) string {
	jsonStart := strings.Index(response, "[")
	if jsonStart > 0 {
		return strings.TrimSpace(response[:jsonStart])
	}
	return strings.TrimSpace(response)
}

// extractDecisions 提取JSON决策
func extractDecisions(response string) ([]Decision, error) {
	arrayStart := strings.Index(response, "[")
	if arrayStart == -1 {
		return nil, fmt.Errorf("无法找到JSON数组起始")
	}

	arrayEnd := findMatchingBracket(response, arrayStart)
	if arrayEnd == -1 {
		return nil, fmt.Errorf("无法找到JSON数组结束")
	}

	jsonContent := strings.TrimSpace(response[arrayStart : arrayEnd+1])
	jsonContent = fixMissingQuotes(jsonContent)

	var decisions []Decision
	if err := json.Unmarshal([]byte(jsonContent), &decisions); err != nil {
		return nil, fmt.Errorf("JSON解析失败: %w\nJSON内容: %s", err, jsonContent)
	}

	return decisions, nil
}

// fixMissingQuotes 修复引号问题
func fixMissingQuotes(jsonStr string) string {
	jsonStr = strings.ReplaceAll(jsonStr, "\u201c", "\"")
	jsonStr = strings.ReplaceAll(jsonStr, "\u201d", "\"")
	jsonStr = strings.ReplaceAll(jsonStr, "\u2018", "'")
	jsonStr = strings.ReplaceAll(jsonStr, "\u2019", "'")
	return jsonStr
}

// findMatchingBracket 查找匹配括号
func findMatchingBracket(s string, start int) int {
	if start >= len(s) || s[start] != '[' {
		return -1
	}

	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// validateDecisionsEnhanced 增强版决策验证
func validateDecisionsEnhanced(decisions []Decision, ctx *Context) error {
	// === 新增：持仓保护期检查 ===
	positionMap := make(map[string]*PositionInfo)
	for i := range ctx.Positions {
		positionMap[ctx.Positions[i].Symbol] = &ctx.Positions[i]
	}
	for _, decision := range decisions {
		if decision.Action == "close_long" || decision.Action == "close_short" || decision.Action == "partial_close" {
			if pos, exists := positionMap[decision.Symbol]; exists {
				holdingMinutes := int64(0)
				if pos.UpdateTime > 0 {
					holdingMinutes = (time.Now().UnixMilli() - pos.UpdateTime) / (1000 * 60)
				}
				// 15分钟保护期
				if holdingMinutes < 15 {
					// 只允许亏损超过1.5%时提前平仓
					if pos.UnrealizedPnLPct > -1.5 {
						return fmt.Errorf("❌ %s 在保护期内（%d/15分钟），禁止平仓",decision.Symbol, holdingMinutes)
					}
				}
			}
		}
	}
	// === 保护期检查结束 ===		
	
	// 统计开仓决策
	newPositions := 0
	sameDirectionHighCorr := make(map[string]int) // 同方向高相关持仓计数

	for i, decision := range decisions {
		if err := validateSingleDecisionEnhanced(&decisions[i], ctx); err != nil {
			return fmt.Errorf("决策 #%d 验证失败: %w", i+1, err)
		}

		// 统计新开仓
		if decision.Action == "open_long" || decision.Action == "open_short" {
			newPositions++

			// 检查相关性
			if corr, ok := ctx.CorrelationMap[decision.Symbol]; ok && corr.IsHighCorr {
				direction := "long"
				if decision.Action == "open_short" {
					direction = "short"
				}
				sameDirectionHighCorr[direction]++
			}
		}
	}

	// 验证总持仓数量
	totalPositions := ctx.Account.PositionCount + newPositions
	if totalPositions > 3 {
		return fmt.Errorf("总持仓数量(%d)超过上限(3)", totalPositions)
	}

	// 验证同方向高相关持仓
	for direction, count := range sameDirectionHighCorr {
		if count > 2 {
			return fmt.Errorf("同方向(%s)高相关持仓(%d)超过上限(2)", direction, count)
		}
	}

	return nil
}

// validateSingleDecisionEnhanced 增强版单决策验证
func validateSingleDecisionEnhanced(d *Decision, ctx *Context) error {
	validActions := map[string]bool{
		"open_long": true, "open_short": true,
		"close_long": true, "close_short": true,
		"update_stop_loss": true, "update_take_profit": true,
		"partial_close": true, "hold": true, "wait": true,
	}

	if !validActions[d.Action] {
		return fmt.Errorf("无效的action: %s", d.Action)
	}

	// 开仓验证
	if d.Action == "open_long" || d.Action == "open_short" {
		availableBalance := ctx.Account.AvailableBalance
		btcEthLeverage := ctx.BTCETHLeverage
		altcoinLeverage := ctx.AltcoinLeverage

		// 杠杆上限
		maxLeverage := altcoinLeverage
		maxPositionValue := availableBalance * float64(altcoinLeverage) * 0.9
		if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
			maxLeverage = btcEthLeverage
			maxPositionValue = availableBalance * float64(btcEthLeverage) * 0.9
		}

		if d.Leverage <= 0 || d.Leverage > maxLeverage {
			return fmt.Errorf("杠杆必须在1-%d之间: %d", maxLeverage, d.Leverage)
		}

		if d.PositionSizeUSD <= 0 {
			return fmt.Errorf("仓位大小必须>0: %.2f", d.PositionSizeUSD)
		}

		// 自动调整仓位
		if d.PositionSizeUSD > maxPositionValue {
			log.Printf("⚠️ 自动调整 %s 仓位: %.0f → %.0f USD", d.Symbol, d.PositionSizeUSD, maxPositionValue*0.9)
			d.PositionSizeUSD = maxPositionValue * 0.9
		}

		// 相关性风险调整
		if corr, ok := ctx.CorrelationMap[d.Symbol]; ok && corr.IsHighCorr {
			adjustedSize := d.PositionSizeUSD * corr.RiskWeight
			if adjustedSize < d.PositionSizeUSD {
				log.Printf("⚠️ 高相关性调整 %s 仓位: %.0f → %.0f USD (权重%.2f)",
					d.Symbol, d.PositionSizeUSD, adjustedSize, corr.RiskWeight)
				d.PositionSizeUSD = adjustedSize
			}
		}

		if d.StopLoss <= 0 || d.TakeProfit <= 0 {
			return fmt.Errorf("止损止盈必须>0")
		}

		// 止损止盈逻辑验证
		if d.Action == "open_long" {
			if d.StopLoss >= d.TakeProfit {
				return fmt.Errorf("做多时止损必须<止盈")
			}
		} else {
			if d.StopLoss <= d.TakeProfit {
				return fmt.Errorf("做空时止损必须>止盈")
			}
		}

		// 风险回报比验证（考虑交易成本）
		marketData, hasData := ctx.MarketDataMap[d.Symbol]
		if !hasData {
			return fmt.Errorf("缺少 %s 市场数据", d.Symbol)
		}

		currentPrice := marketData.CurrentPrice
		var riskPct, rewardPct float64
		if d.Action == "open_long" {
			riskPct = (currentPrice - d.StopLoss) / currentPrice * 100
			rewardPct = (d.TakeProfit - currentPrice) / currentPrice * 100
		} else {
			riskPct = (d.StopLoss - currentPrice) / currentPrice * 100
			rewardPct = (currentPrice - d.TakeProfit) / currentPrice * 100
		}

		// 扣除交易成本（往返约0.2%）
		tradingCost := 0.2
		netRewardPct := rewardPct - tradingCost

		if riskPct <= 0 {
			return fmt.Errorf("风险百分比计算错误: %.2f%%", riskPct)
		}

		riskRewardRatio := netRewardPct / riskPct
		if riskRewardRatio < 2.5 {
			return fmt.Errorf("真实风险回报比过低(%.2f:1 < 2.5:1) [风险:%.2f%% 净收益:%.2f%% 成本:%.2f%%]",
				riskRewardRatio, riskPct, netRewardPct, tradingCost)
		}

		// 单笔风险验证
		positionRiskUSD := d.PositionSizeUSD * (riskPct / 100)
		maxRiskUSD := ctx.Account.TotalEquity * ctx.MaxRiskPerTrade
		// ✅ 添加 1% 容差或 0.01 USD 绝对容差
		tolerance := math.Max(maxRiskUSD*0.01, 0.01)
		if positionRiskUSD > maxRiskUSD + tolerance{
			return fmt.Errorf("单笔风险(%.2f USD)超过上限(%.2f USD, %.1f%%账户净值)",
				positionRiskUSD, maxRiskUSD, ctx.MaxRiskPerTrade*100)
		}
	}

	// 调整止损验证
	if d.Action == "update_stop_loss" {
		if d.NewStopLoss <= 0 {
			return fmt.Errorf("新止损价格必须>0: %.2f", d.NewStopLoss)
		}
	}

	// 调整止盈验证
	if d.Action == "update_take_profit" {
		if d.NewTakeProfit <= 0 {
			return fmt.Errorf("新止盈价格必须>0: %.2f", d.NewTakeProfit)
		}
	}

	// 部分平仓验证
	if d.Action == "partial_close" {
		if d.ClosePercentage <= 0 || d.ClosePercentage > 100 {
			return fmt.Errorf("平仓百分比必须在0-100之间: %.1f", d.ClosePercentage)
		}
	}

	return nil
}

// CalculateOptimalPosition 计算最优仓位（导出函数供外部使用）
func CalculateOptimalPosition(ctx *Context, symbol string, side string) (positionSize, stopLoss, takeProfit float64, err error) {
	marketData, ok := ctx.MarketDataMap[symbol]
	if !ok {
		return 0, 0, 0, fmt.Errorf("缺少 %s 市场数据", symbol)
	}

	currentPrice := marketData.CurrentPrice
	atr14 := marketData.LongerTermContext.ATR14

	// 判断是否山寨币
	isAltcoin := symbol != "BTCUSDT" && symbol != "ETHUSDT"

	// 计算波动率自适应仓位
	suggestedSize, stopDistance := market.CalculateAdaptivePositionSize(
		ctx.Account.TotalEquity,
		atr14,
		currentPrice,
		ctx.MaxRiskPerTrade,
		isAltcoin,
	)

	// 应用相关性风险权重
	if corr, ok := ctx.CorrelationMap[symbol]; ok {
		suggestedSize *= corr.RiskWeight
	}

	// 计算止损止盈
	if side == "long" {
		stopLoss = currentPrice - stopDistance
		takeProfit = currentPrice + stopDistance*3.5 // RR 1:3.5（扣除成本后约1:3）
	} else {
		stopLoss = currentPrice + stopDistance
		takeProfit = currentPrice - stopDistance*3.5
	}

	// 应用杠杆上限
	maxLeverage := ctx.AltcoinLeverage
	maxPositionValue := ctx.Account.AvailableBalance * float64(maxLeverage) * 0.9
	if !isAltcoin {
		maxLeverage = ctx.BTCETHLeverage
		maxPositionValue = ctx.Account.AvailableBalance * float64(maxLeverage) * 0.9
	}

	if suggestedSize > maxPositionValue {
		suggestedSize = maxPositionValue
	}

	return suggestedSize, stopLoss, takeProfit, nil
}

// GetTradingRecommendation 获取交易建议（导出函数）
func GetTradingRecommendation(ctx *Context, symbol string) string {
	marketData, ok := ctx.MarketDataMap[symbol]
	if !ok {
		return "缺少市场数据"
	}

	var recommendations []string

	// 市场状态
	state, confidence := market.GetMarketState(marketData)
	recommendations = append(recommendations, fmt.Sprintf("市场状态: %s (置信度%d%%)", state, confidence))

	// 趋势方向
	if marketData.LongerTermContext.EMA20 > marketData.LongerTermContext.EMA50 {
		recommendations = append(recommendations, "4小时EMA: 多头排列")
	} else {
		recommendations = append(recommendations, "4小时EMA: 空头排列")
	}

	// 资金费率
	fundingPct := marketData.FundingRate * 100
	if fundingPct > 0.05 {
		recommendations = append(recommendations, fmt.Sprintf("资金费率: %.4f%% (做空有利)", fundingPct))
	} else if fundingPct < -0.05 {
		recommendations = append(recommendations, fmt.Sprintf("资金费率: %.4f%% (做多有利)", fundingPct))
	}

	// MACD背离检测
	if marketData.LongerTermContext != nil && len(marketData.LongerTermContext.MACDHist) >= 3 {
		prices := marketData.MidTermSeries1h.MidPrices
		macdHist := marketData.LongerTermContext.MACDHist
		bullDiv, bearDiv := market.DetectDivergence(prices, macdHist)
		if bullDiv {
			recommendations = append(recommendations, "⚠️ 看涨背离信号")
		}
		if bearDiv {
			recommendations = append(recommendations, "⚠️ 看跌背离信号")
		}
	}

	// 相关性
	if corr, ok := ctx.CorrelationMap[symbol]; ok {
		if corr.IsHighCorr {
			recommendations = append(recommendations, fmt.Sprintf("BTC相关性: %.2f (高，需降低仓位)", corr.BTCCorr))
		}
	}

	return strings.Join(recommendations, " | ")
}
