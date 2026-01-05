package decision

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"nofx/market"
	"nofx/mcp"
	"nofx/pool"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// 核心数据结构
// ============================================================================

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
	StopLoss         float64 `json:"stop_loss,omitempty"`
	TakeProfit       float64 `json:"take_profit,omitempty"`
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

// CorrelationData 相关性数据
type CorrelationData struct {
	Symbol     string  `json:"symbol"`
	BTCCorr    float64 `json:"btc_correlation"`
	IsHighCorr bool    `json:"is_high_corr"`
	RiskWeight float64 `json:"risk_weight"`
}

// CircuitBreakerState 熔断状态
type CircuitBreakerState struct {
	IsTriggered     bool      `json:"is_triggered"`
	TriggerReason   string    `json:"trigger_reason"`
	TriggerTime     time.Time `json:"trigger_time"`
	CooldownMinutes int       `json:"cooldown_minutes"`
}

// ============================================================================
// 🆕 优化1: 交易计划持久化到JSON文件
// ============================================================================

// TradePlan 交易计划
type TradePlan struct {
	ID                    string    `json:"id"`
	Symbol                string    `json:"symbol"`
	Direction             string    `json:"direction"`
	EntryPrice            float64   `json:"entry_price"`
	StopLoss              float64   `json:"stop_loss"`
	TakeProfit            float64   `json:"take_profit"`
	PositionSizeUSD       float64   `json:"position_size_usd"`
	Leverage              int       `json:"leverage"`
	EntryReason           string    `json:"entry_reason"`
	InvalidationCondition string    `json:"invalidation_condition"`
	InvalidationPrice     float64   `json:"invalidation_price"`
	MinHoldMinutes        int       `json:"min_hold_minutes"`
	CreatedAt             time.Time `json:"created_at"`
	Status                string    `json:"status"`
	Confidence            int       `json:"confidence"`
	RiskUSD               float64   `json:"risk_usd"`
	PartialCloseAt1R3     bool      `json:"partial_close_at_1r3"`
	PartialCloseAt1R5     bool      `json:"partial_close_at_1r5"`
	TrailingStopActive    bool      `json:"trailing_stop_active"`
	CurrentStopLoss       float64   `json:"current_stop_loss"`
}

// PersistentData 持久化数据结构
type PersistentData struct {
	Plans      map[string]*TradePlan `json:"plans"`
	Statistics *TradeStatistics      `json:"statistics"`
	Returns    []float64             `json:"returns"` // 用于夏普比率计算
	UpdatedAt  time.Time             `json:"updated_at"`
}

// TradePlanManager 交易计划管理器（带持久化）
type TradePlanManager struct {
	plans       map[string]*TradePlan
	mu          sync.RWMutex
	filePath    string
	autoSave    bool
	lastSaveErr error
}

// 全局计划管理器
var planManager *TradePlanManager

// 默认数据目录
const defaultDataDir = "./data"
const plansFileName = "trade_plans.json"

// InitPlanManager 初始化计划管理器
func InitPlanManager(dataDir string) error {
	if dataDir == "" {
		dataDir = defaultDataDir
	}

	// 确保目录存在
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("创建数据目录失败: %w", err)
	}

	filePath := filepath.Join(dataDir, plansFileName)

	planManager = &TradePlanManager{
		plans:    make(map[string]*TradePlan),
		filePath: filePath,
		autoSave: true,
	}

	// 尝试从文件加载
	if err := planManager.loadFromFile(); err != nil {
		log.Printf("⚠️ 加载交易计划失败（可能是首次运行）: %v", err)
	} else {
		log.Printf("📂 成功加载 %d 个交易计划", len(planManager.plans))
	}

	return nil
}

// loadFromFile 从文件加载计划
func (m *TradePlanManager) loadFromFile() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在是正常的
		}
		return err
	}

	var persistentData PersistentData
	if err := json.Unmarshal(data, &persistentData); err != nil {
		return fmt.Errorf("解析JSON失败: %w", err)
	}

	if persistentData.Plans != nil {
		m.plans = persistentData.Plans
	}

	// 恢复统计数据
	if persistentData.Statistics != nil {
		tradeStatsLock.Lock()
		tradeStats = persistentData.Statistics
		tradeStatsLock.Unlock()
	}

	// 恢复收益率序列
	if persistentData.Returns != nil {
		returnsLock.Lock()
		returnsSeries = persistentData.Returns
		returnsLock.Unlock()
	}

	return nil
}

// saveToFile 保存计划到文件
func (m *TradePlanManager) saveToFile() error {
	m.mu.RLock()
	plansCopy := make(map[string]*TradePlan)
	for k, v := range m.plans {
		plansCopy[k] = v
	}
	m.mu.RUnlock()

	tradeStatsLock.RLock()
	statsCopy := *tradeStats
	tradeStatsLock.RUnlock()

	returnsLock.RLock()
	returnsCopy := make([]float64, len(returnsSeries))
	copy(returnsCopy, returnsSeries)
	returnsLock.RUnlock()

	persistentData := PersistentData{
		Plans:      plansCopy,
		Statistics: &statsCopy,
		Returns:    returnsCopy,
		UpdatedAt:  time.Now(),
	}

	data, err := json.MarshalIndent(persistentData, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}

	// 原子写入：先写临时文件，再重命名
	tempFile := m.filePath + ".tmp"
	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	if err := os.Rename(tempFile, m.filePath); err != nil {
		return fmt.Errorf("重命名文件失败: %w", err)
	}

	return nil
}

// autoSaveIfEnabled 自动保存
func (m *TradePlanManager) autoSaveIfEnabled() {
	if !m.autoSave {
		return
	}
	if err := m.saveToFile(); err != nil {
		m.lastSaveErr = err
		log.Printf("⚠️ 自动保存失败: %v", err)
	}
}

// GetPlan 获取交易计划
func (m *TradePlanManager) GetPlan(symbol string) *TradePlan {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.plans[symbol]
}

// SetPlan 设置交易计划（自动持久化）
func (m *TradePlanManager) SetPlan(plan *TradePlan) {
	m.mu.Lock()
	m.plans[plan.Symbol] = plan
	m.mu.Unlock()

	log.Printf("📋 创建交易计划: %s %s @ %.4f, SL=%.4f, TP=%.4f, 最小持仓=%d分钟",
		plan.Symbol, plan.Direction, plan.EntryPrice, plan.StopLoss, plan.TakeProfit, plan.MinHoldMinutes)

	m.autoSaveIfEnabled()
}

// RemovePlan 移除交易计划（自动持久化）
func (m *TradePlanManager) RemovePlan(symbol string) {
	m.mu.Lock()
	if plan, exists := m.plans[symbol]; exists {
		log.Printf("📋 移除交易计划: %s (状态: %s)", symbol, plan.Status)
		delete(m.plans, symbol)
	}
	m.mu.Unlock()

	m.autoSaveIfEnabled()
}

// UpdatePlanStopLoss 更新计划止损（自动持久化）
func (m *TradePlanManager) UpdatePlanStopLoss(symbol string, newSL float64) {
	m.mu.Lock()
	if plan, exists := m.plans[symbol]; exists {
		oldSL := plan.CurrentStopLoss
		if oldSL == 0 {
			oldSL = plan.StopLoss
		}
		plan.CurrentStopLoss = newSL
		plan.TrailingStopActive = true
		log.Printf("📋 更新 %s 止损: %.4f → %.4f", symbol, oldSL, newSL)
	}
	m.mu.Unlock()

	m.autoSaveIfEnabled()
}

// ForceSave 强制保存
func (m *TradePlanManager) ForceSave() error {
	return m.saveToFile()
}

// ============================================================================
// 🆕 优化2: 夏普比率计算
// ============================================================================

var (
	returnsSeries []float64 // 收益率序列
	returnsLock   sync.RWMutex
	riskFreeRate  = 0.0 // 年化无风险利率（可配置）
)

// SharpeConfig 夏普比率配置
type SharpeConfig struct {
	RiskFreeRate     float64 // 年化无风险利率
	AnnualizeFactor  float64 // 年化因子（日收益用252，小时收益用8760）
	MinTradesForCalc int     // 最小交易数量才计算
}

var sharpeConfig = SharpeConfig{
	RiskFreeRate:     0.0,
	AnnualizeFactor:  252, // 假设每日一笔交易
	MinTradesForCalc: 10,
}

// SetSharpeConfig 设置夏普比率配置
func SetSharpeConfig(config SharpeConfig) {
	sharpeConfig = config
}

// AddReturn 添加收益率记录
func AddReturn(returnPct float64) {
	returnsLock.Lock()
	defer returnsLock.Unlock()

	returnsSeries = append(returnsSeries, returnPct)

	// 保留最近1000笔
	if len(returnsSeries) > 1000 {
		returnsSeries = returnsSeries[len(returnsSeries)-1000:]
	}

	// 自动保存
	if planManager != nil {
		planManager.autoSaveIfEnabled()
	}
}

// CalculateSharpeRatio 计算夏普比率
func CalculateSharpeRatio() float64 {
	returnsLock.RLock()
	defer returnsLock.RUnlock()

	if len(returnsSeries) < sharpeConfig.MinTradesForCalc {
		return 0
	}

	// 计算平均收益率
	sum := 0.0
	for _, r := range returnsSeries {
		sum += r
	}
	meanReturn := sum / float64(len(returnsSeries))

	// 计算标准差
	sumSquaredDiff := 0.0
	for _, r := range returnsSeries {
		diff := r - meanReturn
		sumSquaredDiff += diff * diff
	}
	stdDev := math.Sqrt(sumSquaredDiff / float64(len(returnsSeries)))

	if stdDev == 0 {
		return 0
	}

	// 计算周期无风险利率
	periodicRiskFree := sharpeConfig.RiskFreeRate / sharpeConfig.AnnualizeFactor

	// 夏普比率 = (平均收益 - 无风险收益) / 标准差 * sqrt(年化因子)
	sharpe := (meanReturn - periodicRiskFree) / stdDev * math.Sqrt(sharpeConfig.AnnualizeFactor)

	return sharpe
}

// CalculateSortinoRatio 计算索提诺比率（只考虑下行风险）
func CalculateSortinoRatio() float64 {
	returnsLock.RLock()
	defer returnsLock.RUnlock()

	if len(returnsSeries) < sharpeConfig.MinTradesForCalc {
		return 0
	}

	sum := 0.0
	for _, r := range returnsSeries {
		sum += r
	}
	meanReturn := sum / float64(len(returnsSeries))

	// 计算下行标准差（只计算负收益）
	sumSquaredNegative := 0.0
	negativeCount := 0
	for _, r := range returnsSeries {
		if r < 0 {
			sumSquaredNegative += r * r
			negativeCount++
		}
	}

	if negativeCount == 0 {
		return 10.0 // 没有负收益，返回较高值
	}

	downwardStdDev := math.Sqrt(sumSquaredNegative / float64(len(returnsSeries)))

	if downwardStdDev == 0 {
		return 0
	}

	periodicRiskFree := sharpeConfig.RiskFreeRate / sharpeConfig.AnnualizeFactor
	sortino := (meanReturn - periodicRiskFree) / downwardStdDev * math.Sqrt(sharpeConfig.AnnualizeFactor)

	return sortino
}

// GetReturnsStats 获取收益率统计
func GetReturnsStats() map[string]float64 {
	returnsLock.RLock()
	defer returnsLock.RUnlock()

	if len(returnsSeries) == 0 {
		return map[string]float64{
			"count":         0,
			"sharpe_ratio":  0,
			"sortino_ratio": 0,
		}
	}

	sum := 0.0
	positiveSum := 0.0
	negativeSum := 0.0
	positiveCount := 0
	maxReturn := returnsSeries[0]
	minReturn := returnsSeries[0]

	for _, r := range returnsSeries {
		sum += r
		if r > 0 {
			positiveSum += r
			positiveCount++
		} else {
			negativeSum += r
		}
		if r > maxReturn {
			maxReturn = r
		}
		if r < minReturn {
			minReturn = r
		}
	}

	meanReturn := sum / float64(len(returnsSeries))
	winRate := float64(positiveCount) / float64(len(returnsSeries))

	return map[string]float64{
		"count":         float64(len(returnsSeries)),
		"mean_return":   meanReturn,
		"total_return":  sum,
		"max_return":    maxReturn,
		"min_return":    minReturn,
		"win_rate":      winRate,
		"sharpe_ratio":  CalculateSharpeRatio(),
		"sortino_ratio": CalculateSortinoRatio(),
	}
}

// ============================================================================
// 🆕 优化3: 使用正则表达式优化JSON解析
// ============================================================================

// ============================================================================
// 🆕 优化3: 使用正则表达式优化JSON解析（修复版）
// ============================================================================

// JSONExtractor JSON提取器
type JSONExtractor struct {
	arrayPattern  *regexp.Regexp
	objectPattern *regexp.Regexp
}

var jsonExtractor *JSONExtractor

// 中文引号的Unicode常量
const (
	LeftDoubleQuote  = '\u201c' // "
	RightDoubleQuote = '\u201d' // "
	LeftSingleQuote  = '\u2018' // '
	RightSingleQuote = '\u2019' // '
)

func init() {
	jsonExtractor = &JSONExtractor{
		arrayPattern:  regexp.MustCompile(`(?s)\[[\s\S]*?\]`),
		objectPattern: regexp.MustCompile(`(?s)\{[^{}]*\}`),
	}

	// 初始化默认的计划管理器
	if planManager == nil {
		planManager = &TradePlanManager{
			plans:    make(map[string]*TradePlan),
			filePath: filepath.Join(defaultDataDir, plansFileName),
			autoSave: false,
		}
	}
}

// cleanText 清理文本（修复版）
func (e *JSONExtractor) cleanText(text string) string {
	result := text

	// 移除markdown代码块标记
	codeBlockStart := regexp.MustCompile("(?s)```json\\s*")
	codeBlockEnd := regexp.MustCompile("(?s)```\\s*")
	result = codeBlockStart.ReplaceAllString(result, "")
	result = codeBlockEnd.ReplaceAllString(result, "")

	// 替换中文引号为英文引号（使用rune转换）
	result = strings.Map(func(r rune) rune {
		switch r {
		case LeftDoubleQuote, RightDoubleQuote:
			return '"'
		case LeftSingleQuote, RightSingleQuote:
			return '\''
		default:
			return r
		}
	}, result)

	return result
}

// ExtractJSONArray 从文本中提取JSON数组
func (e *JSONExtractor) ExtractJSONArray(text string) (string, error) {
	// 第一步：清理文本
	cleaned := e.cleanText(text)

	// 第二步：找到所有可能的JSON数组
	matches := e.findJSONArrays(cleaned)
	if len(matches) == 0 {
		return "", fmt.Errorf("未找到JSON数组")
	}

	// 第三步：验证并返回第一个有效的JSON数组
	for _, match := range matches {
		fixed := e.fixJSON(match)
		if json.Valid([]byte(fixed)) {
			return fixed, nil
		}
	}

	// 如果没有有效的，尝试修复第一个
	fixed := e.fixJSON(matches[0])
	return fixed, nil
}

// findJSONArrays 查找所有JSON数组
func (e *JSONExtractor) findJSONArrays(text string) []string {
	var results []string

	for i := 0; i < len(text); i++ {
		if text[i] == '[' {
			end := e.findMatchingBracket(text, i)
			if end > i {
				results = append(results, text[i:end+1])
			}
		}
	}

	return results
}

// findMatchingBracket 查找匹配的括号
func (e *JSONExtractor) findMatchingBracket(s string, start int) int {
	if start >= len(s) || s[start] != '[' {
		return -1
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		char := s[i]

		if escaped {
			escaped = false
			continue
		}

		if char == '\\' && inString {
			escaped = true
			continue
		}

		if char == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch char {
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

// fixJSON 修复常见的JSON问题
func (e *JSONExtractor) fixJSON(jsonStr string) string {
	result := jsonStr

	// 1. 移除尾部逗号
	trailingComma := regexp.MustCompile(`,(\s*[\]\}])`)
	result = trailingComma.ReplaceAllString(result, "$1")

	// 2. 修复无引号的key
	unquotedKey := regexp.MustCompile(`([{\[,]\s*)([a-zA-Z_][a-zA-Z0-9_]*)(\s*:)`)
	result = unquotedKey.ReplaceAllString(result, `$1"$2"$3`)

	// 3. 替换特殊值为null
	result = regexp.MustCompile(`\bNaN\b`).ReplaceAllString(result, "null")
	result = regexp.MustCompile(`\bInfinity\b`).ReplaceAllString(result, "null")
	result = regexp.MustCompile(`\b-Infinity\b`).ReplaceAllString(result, "null")
	result = regexp.MustCompile(`\bundefined\b`).ReplaceAllString(result, "null")

	// 4. 修复单引号字符串
	singleQuote := regexp.MustCompile(`'([^']*)'`)
	result = singleQuote.ReplaceAllString(result, `"$1"`)

	// 5. 移除单行注释
	lineComment := regexp.MustCompile(`//[^\n]*`)
	result = lineComment.ReplaceAllString(result, "")

	// 6. 移除多行注释
	blockComment := regexp.MustCompile(`(?s)/\*.*?\*/`)
	result = blockComment.ReplaceAllString(result, "")

	return result
}

// fixMissingQuotes 修复引号问题（使用字节替换）
func fixMissingQuotes(jsonStr string) string {
	// 使用 strings.Map 替换中文引号
	result := strings.Map(func(r rune) rune {
		switch r {
		case '\u201c', '\u201d': // 中文双引号
			return '"'
		case '\u2018', '\u2019': // 中文单引号
			return '\''
		default:
			return r
		}
	}, jsonStr)

	return result
}

// ExtractDecisionsRobust 健壮的决策提取（使用正则）
// ExtractDecisionsRobust 健壮的决策提取（修复版）
func ExtractDecisionsRobust(response string) ([]Decision, string, error) {
	// 使用JSON提取器
	jsonStr, err := jsonExtractor.ExtractJSONArray(response)
	if err != nil {
		return extractDecisionsFallback(response)
	}

	var decisions []Decision
	if err := json.Unmarshal([]byte(jsonStr), &decisions); err != nil {
		decisions, err = parseDecisionsOneByOne(jsonStr)
		if err != nil {
			return nil, "", fmt.Errorf("JSON解析失败: %w", err)
		}
	}

	// 🆕 修复：提取更完整的思维链，包含JSON决策内容
	cotTrace := buildCompleteCotTrace(response, jsonStr)

	return decisions, cotTrace, nil
}

// 🆕 新增：构建完整的思维链（包含JSON决策展示）
func buildCompleteCotTrace(response, jsonStr string) string {
	// 找到JSON数组开始位置
	arrayStart := strings.Index(response, "[")

	// 提取分析部分（JSON之前的内容）
	analysisPart := ""
	if arrayStart > 0 {
		analysisPart = strings.TrimSpace(response[:arrayStart])
	}

	// 清理markdown代码块标记
	analysisPart = strings.TrimSuffix(analysisPart, "```json")
	analysisPart = strings.TrimSuffix(analysisPart, "```")
	analysisPart = strings.TrimSpace(analysisPart)

	// 🆕 将JSON决策格式化后追加到思维链
	var sb strings.Builder
	sb.WriteString(analysisPart)
	sb.WriteString("\n\n**JSON决策内容**:\n```json\n")

	// 格式化JSON以便阅读
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, []byte(jsonStr), "", "  "); err == nil {
		sb.WriteString(prettyJSON.String())
	} else {
		sb.WriteString(jsonStr)
	}
	sb.WriteString("\n```")

	return sb.String()
}

// parseDecisionsOneByOne 逐个解析决策对象
func parseDecisionsOneByOne(jsonStr string) ([]Decision, error) {
	var decisions []Decision

	objectPattern := regexp.MustCompile(`(?s)\{[^{}]*\}`)
	matches := objectPattern.FindAllString(jsonStr, -1)

	for _, match := range matches {
		var d Decision
		if err := json.Unmarshal([]byte(match), &d); err != nil {
			log.Printf("⚠️ 跳过无效决策对象: %v", err)
			continue
		}
		if d.Symbol != "" && d.Action != "" {
			decisions = append(decisions, d)
		}
	}

	if len(decisions) == 0 {
		return nil, fmt.Errorf("未能解析出任何有效决策")
	}

	return decisions, nil
}

// extractDecisionsFallback 回退解析方法
func extractDecisionsFallback(response string) ([]Decision, string, error) {
	cotTrace := extractCoTTrace(response)
	decisions, err := extractDecisions(response)
	return decisions, cotTrace, err
}

// ============================================================================
// 持仓评估器
// ============================================================================

// PositionEvaluator 持仓评估器
type PositionEvaluator struct {
	Position   *PositionInfo
	Plan       *TradePlan
	MarketData *market.Data
}

// EvaluationResult 评估结果
type EvaluationResult struct {
	Action            string
	Reason            string
	NewStopLoss       float64
	ClosePercentage   float64
	IsHardStop        bool
	IsPlanInvalidated bool
}

// Evaluate 评估持仓
func (e *PositionEvaluator) Evaluate() *EvaluationResult {
	result := &EvaluationResult{Action: "hold", Reason: "继续持有"}

	if e.Position == nil || e.MarketData == nil {
		return result
	}

	currentPrice := e.MarketData.CurrentPrice
	holdingMinutes := e.getHoldingMinutes()

	// ========== 第一优先级：硬性止损/止盈检查 ==========
	if e.Plan != nil {
		effectiveSL := e.Plan.CurrentStopLoss
		if effectiveSL == 0 {
			effectiveSL = e.Plan.StopLoss
		}

		// 检查止损
		if e.Plan.Direction == "long" && currentPrice <= effectiveSL {
			return &EvaluationResult{
				Action:     "close",
				Reason:     fmt.Sprintf("触发止损: 当前价%.4f <= 止损价%.4f", currentPrice, effectiveSL),
				IsHardStop: true,
			}
		}
		if e.Plan.Direction == "short" && currentPrice >= effectiveSL {
			return &EvaluationResult{
				Action:     "close",
				Reason:     fmt.Sprintf("触发止损: 当前价%.4f >= 止损价%.4f", currentPrice, effectiveSL),
				IsHardStop: true,
			}
		}

		// 检查止盈
		if e.Plan.Direction == "long" && currentPrice >= e.Plan.TakeProfit {
			return &EvaluationResult{
				Action:     "close",
				Reason:     fmt.Sprintf("触发止盈: 当前价%.4f >= 止盈价%.4f", currentPrice, e.Plan.TakeProfit),
				IsHardStop: true,
			}
		}
		if e.Plan.Direction == "short" && currentPrice <= e.Plan.TakeProfit {
			return &EvaluationResult{
				Action:     "close",
				Reason:     fmt.Sprintf("触发止盈: 当前价%.4f <= 止盈价%.4f", currentPrice, e.Plan.TakeProfit),
				IsHardStop: true,
			}
		}
	}

	// ========== 第二优先级：最小持仓时间保护 ==========
	minHoldMinutes := 30
	if e.Plan != nil && e.Plan.MinHoldMinutes > 0 {
		minHoldMinutes = e.Plan.MinHoldMinutes
	}

	if holdingMinutes < int64(minHoldMinutes) {
		if e.Position.UnrealizedPnLPct < -3.0 {
			return &EvaluationResult{
				Action:     "close",
				Reason:     fmt.Sprintf("保护期内极端亏损(%.2f%% < -3%%)，紧急平仓", e.Position.UnrealizedPnLPct),
				IsHardStop: true,
			}
		}
		result.Reason = fmt.Sprintf("持仓保护期(%d/%d分钟)，继续持有", holdingMinutes, minHoldMinutes)
		return result
	}

	// ========== 第三优先级：移动止损检查 ==========
	if e.Position.UnrealizedPnLPct > 0 && e.Plan != nil {
		newSL := e.calculateTrailingStop()
		effectiveSL := e.Plan.CurrentStopLoss
		if effectiveSL == 0 {
			effectiveSL = e.Plan.StopLoss
		}

		if newSL > 0 && newSL != effectiveSL {
			shouldUpdate := false
			if e.Plan.Direction == "long" && newSL > effectiveSL {
				shouldUpdate = true
			}
			if e.Plan.Direction == "short" && newSL < effectiveSL {
				shouldUpdate = true
			}

			if shouldUpdate {
				return &EvaluationResult{
					Action:      "update_stop_loss",
					NewStopLoss: newSL,
					Reason:      fmt.Sprintf("移动止损: %.4f → %.4f (盈利%.2f%%)", effectiveSL, newSL, e.Position.UnrealizedPnLPct),
				}
			}
		}
	}

	// ========== 第四优先级：分批止盈检查 ==========
	if e.Plan != nil && e.Position.UnrealizedPnLPct > 0 {
		riskDistance := math.Abs(e.Plan.EntryPrice - e.Plan.StopLoss)
		if riskDistance > 0 {
			currentDistance := math.Abs(currentPrice - e.Plan.EntryPrice)
			currentRR := currentDistance / riskDistance

			if currentRR >= 3.0 && !e.Plan.PartialCloseAt1R3 {
				return &EvaluationResult{
					Action:          "partial_close",
					ClosePercentage: 50,
					Reason:          fmt.Sprintf("达到RR 1:3 (当前%.2f:1)，平仓50%%", currentRR),
				}
			}

			if currentRR >= 5.0 && !e.Plan.PartialCloseAt1R5 && e.Plan.PartialCloseAt1R3 {
				return &EvaluationResult{
					Action:          "partial_close",
					ClosePercentage: 30,
					Reason:          fmt.Sprintf("达到RR 1:5 (当前%.2f:1)，平仓30%%", currentRR),
				}
			}
		}
	}

	// ========== 第五优先级：计划失效条件检查 ==========
	if holdingMinutes >= 60 && e.Plan != nil {
		if invalidated, reason := e.checkPlanInvalidation(); invalidated {
			return &EvaluationResult{
				Action:            "close",
				Reason:            reason,
				IsPlanInvalidated: true,
			}
		}
	}

	return result
}

// getHoldingMinutes 获取持仓时长
func (e *PositionEvaluator) getHoldingMinutes() int64 {
	if e.Position.UpdateTime <= 0 {
		return 0
	}
	return (time.Now().UnixMilli() - e.Position.UpdateTime) / (1000 * 60)
}

// calculateTrailingStop 计算移动止损
func (e *PositionEvaluator) calculateTrailingStop() float64 {
	if e.Plan == nil {
		return 0
	}

	pnlPct := e.Position.UnrealizedPnLPct
	entryPrice := e.Plan.EntryPrice

	var newSL float64

	if e.Plan.Direction == "long" {
		if pnlPct >= 15 {
			newSL = entryPrice * 1.05
		} else if pnlPct >= 10 {
			newSL = entryPrice * 1.02
		} else if pnlPct >= 7 {
			newSL = entryPrice
		}
	} else {
		if pnlPct >= 15 {
			newSL = entryPrice * 0.95
		} else if pnlPct >= 10 {
			newSL = entryPrice * 0.98
		} else if pnlPct >= 7 {
			newSL = entryPrice
		}
	}

	return newSL
}

// checkPlanInvalidation 检查计划是否失效
func (e *PositionEvaluator) checkPlanInvalidation() (bool, string) {
	if e.MarketData == nil {
		return false, ""
	}

	if market.Is4HTrendReversed(e.MarketData, e.Plan.Direction) {
		adx, diPlus, diMinus := market.GetTrendInfo(e.MarketData)

		if e.Plan.Direction == "long" {
			return true, fmt.Sprintf("4H趋势反转(ADX=%.1f, DI-=%.1f > DI+=%.1f)，计划失效",
				adx, diMinus, diPlus)
		} else {
			return true, fmt.Sprintf("4H趋势反转(ADX=%.1f, DI+=%.1f > DI-=%.1f)，计划失效",
				adx, diPlus, diMinus)
		}
	}

	if e.MarketData.LongerTermContext != nil {
		ctx := e.MarketData.LongerTermContext
		if e.Plan.Direction == "long" && ctx.EMA20 < ctx.EMA50 {
			return true, fmt.Sprintf("4H EMA死叉(EMA20=%.2f < EMA50=%.2f)，计划失效",
				ctx.EMA20, ctx.EMA50)
		}
		if e.Plan.Direction == "short" && ctx.EMA20 > ctx.EMA50 {
			return true, fmt.Sprintf("4H EMA金叉(EMA20=%.2f > EMA50=%.2f)，计划失效",
				ctx.EMA20, ctx.EMA50)
		}
	}

	if e.Plan.InvalidationPrice > 0 {
		currentPrice := e.MarketData.CurrentPrice
		if e.Plan.Direction == "long" && currentPrice < e.Plan.InvalidationPrice {
			return true, fmt.Sprintf("价格跌破失效线(%.4f < %.4f)，计划失效",
				currentPrice, e.Plan.InvalidationPrice)
		}
		if e.Plan.Direction == "short" && currentPrice > e.Plan.InvalidationPrice {
			return true, fmt.Sprintf("价格突破失效线(%.4f > %.4f)，计划失效",
				currentPrice, e.Plan.InvalidationPrice)
		}
	}

	return false, ""
}

// ============================================================================
// Context 交易上下文
// ============================================================================

// Context 交易上下文
type Context struct {
	CurrentTime         string                      `json:"current_time"`
	RuntimeMinutes      int                         `json:"runtime_minutes"`
	CallCount           int                         `json:"call_count"`
	Account             AccountInfo                 `json:"account"`
	Positions           []PositionInfo              `json:"positions"`
	CandidateCoins      []CandidateCoin             `json:"candidate_coins"`
	MarketDataMap       map[string]*market.Data     `json:"-"`
	OITopDataMap        map[string]*OITopData       `json:"-"`
	CorrelationMap      map[string]*CorrelationData `json:"-"`
	CircuitBreaker      *CircuitBreakerState        `json:"-"`
	Performance         interface{}                 `json:"-"`
	BTCETHLeverage      int                         `json:"-"`
	AltcoinLeverage     int                         `json:"-"`
	MaxRiskPerTrade     float64                     `json:"-"`
	TotalRiskBudget     float64                     `json:"-"`
	LastAnalysisTime    time.Time                   `json:"-"`
	AnalysisIntervalMin int                         `json:"-"`
}

// Decision AI的交易决策
type Decision struct {
	Symbol                string  `json:"symbol"`
	Action                string  `json:"action"`
	Leverage              int     `json:"leverage,omitempty"`
	PositionSizeUSD       float64 `json:"position_size_usd,omitempty"`
	StopLoss              float64 `json:"stop_loss,omitempty"`
	TakeProfit            float64 `json:"take_profit,omitempty"`
	NewStopLoss           float64 `json:"new_stop_loss,omitempty"`
	NewTakeProfit         float64 `json:"new_take_profit,omitempty"`
	ClosePercentage       float64 `json:"close_percentage,omitempty"`
	Confidence            int     `json:"confidence,omitempty"`
	RiskUSD               float64 `json:"risk_usd,omitempty"`
	Reasoning             string  `json:"reasoning"`
	InvalidationPrice     float64 `json:"invalidation_price,omitempty"`
	InvalidationCondition string  `json:"invalidation_condition,omitempty"`
	MinHoldMinutes        int     `json:"min_hold_minutes,omitempty"`
}

// FullDecision AI的完整决策
type FullDecision struct {
	UserPrompt string     `json:"user_prompt"`
	CoTTrace   string     `json:"cot_trace"`
	Decisions  []Decision `json:"decisions"`
	Timestamp  time.Time  `json:"timestamp"`
}

// ============================================================================
// 核心决策函数
// ============================================================================

// GetFullDecision 获取AI的完整交易决策
func GetFullDecision(ctx *Context, mcpClient *mcp.Client) (*FullDecision, error) {
	initializeDefaults(ctx)

	if result := checkCircuitBreaker(ctx); result != nil {
		return result, nil
	}

	if err := fetchMarketDataForContext(ctx); err != nil {
		return nil, fmt.Errorf("获取市场数据失败: %w", err)
	}

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

	calculateCorrelationMatrix(ctx)

	positionDecisions := evaluateExistingPositions(ctx)

	shouldCallAI := shouldCallAIForNewOpportunities(ctx)

	var aiDecisions []Decision
	var cotTrace string

	if shouldCallAI {
		remainingBudget := calculateRemainingRiskBudget(ctx)
		if remainingBudget <= 0 {
			log.Printf("⚠️ 风险预算已用尽(剩余%.2f%%)，跳过新机会搜索", remainingBudget*100)
		} else {
			systemPrompt := buildSystemPromptOptimized(ctx)
			userPrompt := buildUserPromptOptimized(ctx, remainingBudget)

			aiResponse, err := mcpClient.CallWithMessages(systemPrompt, userPrompt)
			if err != nil {
				log.Printf("⚠️ 调用AI API失败: %v", err)
			} else {
				// 使用优化后的JSON解析
				aiDecisions, cotTrace, _ = ExtractDecisionsRobust(aiResponse)

				// 验证决策
				var validDecisions []Decision
				for _, d := range aiDecisions {
					if d.Action == "open_long" || d.Action == "open_short" {
						if err := validateOpenDecision(&d, ctx); err != nil {
							log.Printf("⚠️ 开仓决策验证失败: %v", err)
							continue
						}
						validDecisions = append(validDecisions, d)
					} else if d.Action == "wait" {
						validDecisions = append(validDecisions, d)
					}
				}
				aiDecisions = validDecisions
			}
		}

		ctx.LastAnalysisTime = time.Now()
	}

	allDecisions := mergeDecisions(positionDecisions, aiDecisions)

	if err := validateFinalDecisions(allDecisions, ctx); err != nil {
		log.Printf("⚠️ 决策验证警告: %v", err)
	}

	return &FullDecision{
		CoTTrace:  cotTrace,
		Decisions: allDecisions,
		Timestamp: time.Now(),
	}, nil
}

// initializeDefaults 初始化默认参数
func initializeDefaults(ctx *Context) {
	if ctx.MaxRiskPerTrade == 0 {
		ctx.MaxRiskPerTrade = 0.02
	}
	if ctx.TotalRiskBudget == 0 {
		ctx.TotalRiskBudget = 0.08
	}
	if ctx.AnalysisIntervalMin == 0 {
		ctx.AnalysisIntervalMin = 15
	}
}

// checkCircuitBreaker 检查熔断状态
func checkCircuitBreaker(ctx *Context) *FullDecision {
	if ctx.CircuitBreaker != nil && ctx.CircuitBreaker.IsTriggered {
		cooldownEnd := ctx.CircuitBreaker.TriggerTime.Add(
			time.Duration(ctx.CircuitBreaker.CooldownMinutes) * time.Minute)
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
			}
		}
		ctx.CircuitBreaker.IsTriggered = false
	}
	return nil
}

// evaluateExistingPositions 基于计划评估现有持仓
func evaluateExistingPositions(ctx *Context) []Decision {
	var decisions []Decision

	for _, pos := range ctx.Positions {
		plan := planManager.GetPlan(pos.Symbol)
		marketData := ctx.MarketDataMap[pos.Symbol]

		evaluator := &PositionEvaluator{
			Position:   &pos,
			Plan:       plan,
			MarketData: marketData,
		}

		result := evaluator.Evaluate()

		switch result.Action {
		case "close":
			action := "close_long"
			if pos.Side == "short" {
				action = "close_short"
			}
			decisions = append(decisions, Decision{
				Symbol:    pos.Symbol,
				Action:    action,
				Reasoning: result.Reason,
			})
			if result.IsPlanInvalidated && plan != nil {
				plan.Status = "INVALIDATED"
			}
			planManager.RemovePlan(pos.Symbol)

		case "partial_close":
			decisions = append(decisions, Decision{
				Symbol:          pos.Symbol,
				Action:          "partial_close",
				ClosePercentage: result.ClosePercentage,
				Reasoning:       result.Reason,
			})
			if plan != nil {
				if result.ClosePercentage == 50 {
					plan.PartialCloseAt1R3 = true
				} else if result.ClosePercentage == 30 {
					plan.PartialCloseAt1R5 = true
				}
				planManager.autoSaveIfEnabled()
			}

		case "update_stop_loss":
			decisions = append(decisions, Decision{
				Symbol:      pos.Symbol,
				Action:      "update_stop_loss",
				NewStopLoss: result.NewStopLoss,
				Reasoning:   result.Reason,
			})
			planManager.UpdatePlanStopLoss(pos.Symbol, result.NewStopLoss)

		case "hold":
			decisions = append(decisions, Decision{
				Symbol:    pos.Symbol,
				Action:    "hold",
				Reasoning: result.Reason,
			})
		}
	}

	return decisions
}

// shouldCallAIForNewOpportunities 判断是否应该调用AI寻找新机会
func shouldCallAIForNewOpportunities(ctx *Context) bool {
	if !ctx.LastAnalysisTime.IsZero() {
		elapsed := time.Since(ctx.LastAnalysisTime).Minutes()
		if elapsed < float64(ctx.AnalysisIntervalMin) {
			log.Printf("📊 距离上次分析%.1f分钟，跳过AI调用(间隔%d分钟)", elapsed, ctx.AnalysisIntervalMin)
			return false
		}
	}

	if ctx.Account.PositionCount >= 3 {
		log.Printf("📊 持仓已满(%d/3)，跳过新机会搜索", ctx.Account.PositionCount)
		return false
	}

	remainingBudget := calculateRemainingRiskBudget(ctx)
	if remainingBudget <= 0.01 {
		log.Printf("📊 风险预算不足(剩余%.2f%%)，跳过新机会搜索", remainingBudget*100)
		return false
	}

	return true
}

// calculateRemainingRiskBudget 计算剩余风险预算
func calculateRemainingRiskBudget(ctx *Context) float64 {
	usedRisk := calculateUsedRisk(ctx)
	return ctx.TotalRiskBudget - usedRisk
}

// mergeDecisions 合并决策
func mergeDecisions(positionDecisions, aiDecisions []Decision) []Decision {
	decisionMap := make(map[string]Decision)

	for _, d := range positionDecisions {
		decisionMap[d.Symbol] = d
	}

	for _, d := range aiDecisions {
		if d.Action == "open_long" || d.Action == "open_short" {
			if existing, exists := decisionMap[d.Symbol]; exists {
				if existing.Action == "hold" || existing.Action == "wait" {
					continue
				}
			}
			decisionMap[d.Symbol] = d
		} else if d.Action == "wait" && len(positionDecisions) == 0 {
			decisionMap[d.Symbol] = d
		}
	}

	var result []Decision
	for _, d := range decisionMap {
		result = append(result, d)
	}

	return result
}

// validateOpenDecision 验证开仓决策
func validateOpenDecision(d *Decision, ctx *Context) error {
	for _, pos := range ctx.Positions {
		if pos.Symbol == d.Symbol {
			return fmt.Errorf("%s 已有持仓，不能重复开仓", d.Symbol)
		}
	}

	remainingBudget := calculateRemainingRiskBudget(ctx)
	estimatedRisk := d.RiskUSD / ctx.Account.TotalEquity
	if estimatedRisk > remainingBudget {
		return fmt.Errorf("风险预算不足: 需要%.2f%%, 剩余%.2f%%", estimatedRisk*100, remainingBudget*100)
	}

	maxLeverage := ctx.AltcoinLeverage
	if d.Symbol == "BTCUSDT" || d.Symbol == "ETHUSDT" {
		maxLeverage = ctx.BTCETHLeverage
	}
	if d.Leverage <= 0 || d.Leverage > maxLeverage {
		return fmt.Errorf("杠杆必须在1-%d之间: %d", maxLeverage, d.Leverage)
	}

	if d.PositionSizeUSD <= 0 {
		return fmt.Errorf("仓位大小必须>0")
	}

	maxPositionValue := ctx.Account.AvailableBalance * float64(maxLeverage) * 0.9
	if d.PositionSizeUSD > maxPositionValue {
		log.Printf("⚠️ 自动调整仓位: %.0f → %.0f USD", d.PositionSizeUSD, maxPositionValue*0.9)
		d.PositionSizeUSD = maxPositionValue * 0.9
	}

	if corr, ok := ctx.CorrelationMap[d.Symbol]; ok && corr.IsHighCorr {
		adjustedSize := d.PositionSizeUSD * corr.RiskWeight
		log.Printf("⚠️ 高相关性调整: %.0f → %.0f USD", d.PositionSizeUSD, adjustedSize)
		d.PositionSizeUSD = adjustedSize
	}

	if d.StopLoss <= 0 || d.TakeProfit <= 0 {
		return fmt.Errorf("止损止盈必须>0")
	}

	marketData, ok := ctx.MarketDataMap[d.Symbol]
	if !ok {
		return fmt.Errorf("缺少 %s 市场数据", d.Symbol)
	}

	currentPrice := marketData.CurrentPrice
	var riskPct, rewardPct float64
	if d.Action == "open_long" {
		if d.StopLoss >= currentPrice || d.TakeProfit <= currentPrice {
			return fmt.Errorf("做多止损必须<当前价<止盈")
		}
		riskPct = (currentPrice - d.StopLoss) / currentPrice * 100
		rewardPct = (d.TakeProfit - currentPrice) / currentPrice * 100
	} else {
		if d.StopLoss <= currentPrice || d.TakeProfit >= currentPrice {
			return fmt.Errorf("做空止损必须>当前价>止盈")
		}
		riskPct = (d.StopLoss - currentPrice) / currentPrice * 100
		rewardPct = (currentPrice - d.TakeProfit) / currentPrice * 100
	}

	tradingCost := 0.2
	netRewardPct := rewardPct - tradingCost
	riskRewardRatio := netRewardPct / riskPct

	if riskRewardRatio < 2.5 {
		return fmt.Errorf("风险回报比过低(%.2f:1 < 2.5:1)", riskRewardRatio)
	}

	positionRiskUSD := d.PositionSizeUSD * (riskPct / 100)
	maxRiskUSD := ctx.Account.TotalEquity * ctx.MaxRiskPerTrade
	if positionRiskUSD > maxRiskUSD*1.01 {
		return fmt.Errorf("单笔风险(%.2f USD)超过上限(%.2f USD)", positionRiskUSD, maxRiskUSD)
	}

	d.RiskUSD = positionRiskUSD
	return nil
}

// validateFinalDecisions 验证最终决策
func validateFinalDecisions(decisions []Decision, ctx *Context) error {
	newPositions := 0
	for _, d := range decisions {
		if d.Action == "open_long" || d.Action == "open_short" {
			newPositions++
		}
	}

	totalPositions := ctx.Account.PositionCount + newPositions
	if totalPositions > 3 {
		return fmt.Errorf("总持仓数量(%d)超过上限(3)", totalPositions)
	}

	return nil
}

// CreateTradePlanFromDecision 从决策创建交易计划
func CreateTradePlanFromDecision(d *Decision, currentPrice float64) *TradePlan {
	direction := "long"
	if d.Action == "open_short" {
		direction = "short"
	}

	plan := &TradePlan{
		ID:                    fmt.Sprintf("%s_%d", d.Symbol, time.Now().UnixNano()),
		Symbol:                d.Symbol,
		Direction:             direction,
		EntryPrice:            currentPrice,
		StopLoss:              d.StopLoss,
		TakeProfit:            d.TakeProfit,
		CurrentStopLoss:       d.StopLoss,
		PositionSizeUSD:       d.PositionSizeUSD,
		Leverage:              d.Leverage,
		EntryReason:           d.Reasoning,
		InvalidationCondition: d.InvalidationCondition,
		InvalidationPrice:     d.InvalidationPrice,
		MinHoldMinutes:        d.MinHoldMinutes,
		CreatedAt:             time.Now(),
		Status:                "ACTIVE",
		Confidence:            d.Confidence,
		RiskUSD:               d.RiskUSD,
	}

	if plan.MinHoldMinutes == 0 {
		plan.MinHoldMinutes = 30
	}

	planManager.SetPlan(plan)
	return plan
}

// ============================================================================
// 熔断机制
// ============================================================================

func shouldTriggerCircuitBreaker(ctx *Context) bool {
	if ctx.CircuitBreaker == nil {
		ctx.CircuitBreaker = &CircuitBreakerState{}
	}

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

	if ctx.Account.TotalPnLPct < -10.0 {
		ctx.CircuitBreaker.IsTriggered = true
		ctx.CircuitBreaker.TriggerReason = fmt.Sprintf("账户回撤 %.2f%% 超过10%%", ctx.Account.TotalPnLPct)
		ctx.CircuitBreaker.TriggerTime = time.Now()
		ctx.CircuitBreaker.CooldownMinutes = 60
		log.Printf("🛑 熔断触发: %s", ctx.CircuitBreaker.TriggerReason)
		return true
	}

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

// ============================================================================
// 相关性计算
// ============================================================================

func calculateCorrelationMatrix(ctx *Context) {
	ctx.CorrelationMap = make(map[string]*CorrelationData)

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
			riskWeight = 0.7
		} else if math.Abs(corr) < 0.5 {
			riskWeight = 1.0
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

// ============================================================================
// 市场数据获取
// ============================================================================

func fetchMarketDataForContext(ctx *Context) error {
	ctx.MarketDataMap = make(map[string]*market.Data)
	ctx.OITopDataMap = make(map[string]*OITopData)

	symbolSet := make(map[string]bool)
	symbolSet["BTCUSDT"] = true

	for _, pos := range ctx.Positions {
		symbolSet[pos.Symbol] = true
	}

	for _, coin := range ctx.CandidateCoins {
		symbolSet[coin.Symbol] = true
	}

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

// ============================================================================
// 风险计算
// ============================================================================

func calculateUsedRisk(ctx *Context) float64 {
	if ctx.Account.TotalEquity <= 0 {
		return 0
	}

	totalRisk := 0.0
	for _, pos := range ctx.Positions {
		posRisk := pos.MarginUsed / ctx.Account.TotalEquity
		totalRisk += posRisk
	}

	return totalRisk
}

// ============================================================================
// JSON解析辅助函数（保留旧方法作为回退）
// ============================================================================

func extractCoTTrace(response string) string {
	jsonStart := strings.Index(response, "[")
	if jsonStart > 0 {
		return strings.TrimSpace(response[:jsonStart])
	}
	return strings.TrimSpace(response)
}

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

// ============================================================================
// System/User Prompt 构建
// ============================================================================

func buildSystemPromptOptimized(ctx *Context) string {
	var sb strings.Builder

	availableBalance := ctx.Account.AvailableBalance
	btcEthLeverage := ctx.BTCETHLeverage
	altcoinLeverage := ctx.AltcoinLeverage

	// 获取当前夏普比率
	sharpeRatio := CalculateSharpeRatio()

	sb.WriteString("你是专业的加密货币交易AI，核心目标是**最大化夏普比率**。\n\n")

	// 添加当前夏普比率状态
	if sharpeRatio != 0 {
		sb.WriteString(fmt.Sprintf("**当前策略夏普比率**: %.2f\n", sharpeRatio))
		if sharpeRatio < 0 {
			sb.WriteString("⚠️ 夏普比率为负，需要更加保守的策略\n")
		} else if sharpeRatio > 1.5 {
			sb.WriteString("✅ 夏普比率良好，可以适当增加交易频率\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("# 🎯 你的核心职责\n\n")
	sb.WriteString("**只负责寻找新的开仓机会**。持仓管理由系统自动执行。\n\n")
	sb.WriteString("**量化标准**:\n")
	sb.WriteString("- 每天2-4笔开仓\n")
	sb.WriteString("- 只输出高置信度(≥80)的开仓决策\n")
	sb.WriteString("- 没有好机会时，输出 `wait`\n\n")

	maxPositionForAltcoin := availableBalance * float64(altcoinLeverage) * 0.9
	maxPositionForBTCETH := availableBalance * float64(btcEthLeverage) * 0.9

	sb.WriteString("# ⚖️ 硬约束\n\n")
	sb.WriteString("| 约束 | 值 |\n")
	sb.WriteString("|------|----|\n")
	sb.WriteString("| 风险回报比 | ≥ 1:3 |\n")
	sb.WriteString("| 单笔风险 | ≤ 账户净值的2% |\n")
	sb.WriteString(fmt.Sprintf("| 仓位上限 | 山寨币 %.0f USD / BTC&ETH %.0f USD |\n", maxPositionForAltcoin, maxPositionForBTCETH))
	sb.WriteString("| OI价值 | ≥ 15M USD |\n\n")

	sb.WriteString("# 📋 开仓决策流程\n\n")
	sb.WriteString("1. **评估BTC趋势** → 确定大方向\n")
	sb.WriteString("2. **筛选候选币种** → ADX>25 + 趋势方向一致\n")
	sb.WriteString("3. **多时间框架确认** → 4h/1h/15m 信号对齐\n")
	sb.WriteString("4. **计算仓位** → ATR自适应 + 相关性调整\n")
	sb.WriteString("5. **设置止损止盈** → 止损=ATR×2.5, RR≥1:3.5\n")
	sb.WriteString("6. **定义失效条件** → 什么情况下计划失效\n\n")

	sb.WriteString("# 💵 波动率自适应仓位\n\n")
	sb.WriteString("```\n")
	sb.WriteString("止损距离 = ATR14 × 倍数（山寨2.5，BTC/ETH 1.8）\n")
	sb.WriteString("仓位大小 = (账户净值 × 2%) / 止损百分比\n")
	sb.WriteString("```\n\n")

	sb.WriteString("# 📐 相关性控制\n\n")
	sb.WriteString("- 高相关(ρ>0.8)：仓位×0.7\n")
	sb.WriteString("- 中相关(0.5<ρ<0.8)：仓位×0.85\n")
	sb.WriteString("- 同方向高相关持仓不超过2个\n\n")

	sb.WriteString("# 📤 输出格式\n\n")
	sb.WriteString("**第一步**: 简短分析（3-5句话）\n")
	sb.WriteString("**第二步**: JSON决策数组\n\n")

	sb.WriteString("**开仓决策JSON**:\n")
	sb.WriteString("```json\n")
	sb.WriteString("[\n")
	sb.WriteString("  {\n")
	sb.WriteString("    \"symbol\": \"BTCUSDT\",\n")
	sb.WriteString("    \"action\": \"open_long\",\n")
	sb.WriteString(fmt.Sprintf("    \"leverage\": %d,\n", btcEthLeverage))
	sb.WriteString("    \"position_size_usd\": 100,\n")
	sb.WriteString("    \"stop_loss\": 95000,\n")
	sb.WriteString("    \"take_profit\": 105000,\n")
	sb.WriteString("    \"confidence\": 85,\n")
	sb.WriteString("    \"risk_usd\": 10,\n")
	sb.WriteString("    \"invalidation_price\": 96000,\n")
	sb.WriteString("    \"invalidation_condition\": \"4H收盘跌破EMA50\",\n")
	sb.WriteString("    \"min_hold_minutes\": 30,\n")
	sb.WriteString("    \"reasoning\": \"BTC强势+4H突破+资金费率中性\"\n")
	sb.WriteString("  }\n")
	sb.WriteString("]\n")
	sb.WriteString("```\n\n")

	sb.WriteString("**无机会时**:\n")
	sb.WriteString("```json\n")
	sb.WriteString("[{\"symbol\": \"ALL\", \"action\": \"wait\", \"reasoning\": \"无符合条件的机会\"}]\n")
	sb.WriteString("```\n\n")

	sb.WriteString("---\n")
	sb.WriteString("**核心原则**: 宁可错过，不可做错 | 风险回报比≥1:3 | BTC是龙头\n")

	return sb.String()
}

func buildUserPromptOptimized(ctx *Context, remainingBudget float64) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("**时间**: %s | **周期**: #%d\n\n", ctx.CurrentTime, ctx.CallCount))

	// BTC状态
	if btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]; hasBTC {
		marketState, stateConfidence := market.GetMarketState(btcData)
		sb.WriteString("## 🪙 BTC状态\n")
		sb.WriteString(fmt.Sprintf("**价格**: %.2f | **趋势**: **%s** (置信度%d%%)\n",
			btcData.CurrentPrice, marketState, stateConfidence))
		sb.WriteString(fmt.Sprintf("**ADX**: %.1f | **DI+**: %.1f | **DI-**: %.1f\n",
			btcData.CurrentADX, btcData.CurrentDIPlus, btcData.CurrentDIMinus))
		sb.WriteString(fmt.Sprintf("**MACD**: %.4f | **RSI14**: %.1f | **资金费率**: %.4f%%\n\n",
			btcData.CurrentMACD, btcData.CurrentRSI14, btcData.FundingRate*100))
	}

	// 账户状态（包含夏普比率）
	sb.WriteString("## 💰 账户状态\n")
	sb.WriteString(fmt.Sprintf("**净值**: %.2f USDT | **可用**: %.2f USDT\n",
		ctx.Account.TotalEquity, ctx.Account.AvailableBalance))
	sb.WriteString(fmt.Sprintf("**剩余风险预算**: **%.1f%%** (可开仓风险额度)\n",
		remainingBudget*100))
	sb.WriteString(fmt.Sprintf("**当前持仓**: %d/3\n", ctx.Account.PositionCount))

	// 显示夏普比率
	sharpeRatio := CalculateSharpeRatio()
	sortinoRatio := CalculateSortinoRatio()
	if sharpeRatio != 0 || sortinoRatio != 0 {
		sb.WriteString(fmt.Sprintf("**夏普比率**: %.2f | **索提诺比率**: %.2f\n", sharpeRatio, sortinoRatio))
	}
	sb.WriteString("\n")

	// 当前持仓
	if len(ctx.Positions) > 0 {
		sb.WriteString("## 📊 当前持仓（仅供参考，不需要管理）\n")
		for _, pos := range ctx.Positions {
			sb.WriteString(fmt.Sprintf("- %s %s: 盈亏 %+.2f%%\n",
				pos.Symbol, strings.ToUpper(pos.Side), pos.UnrealizedPnLPct))
		}
		sb.WriteString("\n")
	}

	// 候选币种
	sb.WriteString("## 🔍 候选币种\n\n")
	displayedCount := 0
	for _, coin := range ctx.CandidateCoins {
		if coin.Symbol == "BTCUSDT" {
			continue
		}
		hasPosition := false
		for _, pos := range ctx.Positions {
			if pos.Symbol == coin.Symbol {
				hasPosition = true
				break
			}
		}
		if hasPosition {
			continue
		}

		marketData, hasData := ctx.MarketDataMap[coin.Symbol]
		if !hasData {
			continue
		}
		displayedCount++

		corrInfo := ""
		if corr, ok := ctx.CorrelationMap[coin.Symbol]; ok {
			corrInfo = fmt.Sprintf(" | BTC相关性: %.2f", corr.BTCCorr)
			if corr.IsHighCorr {
				corrInfo += "(高)"
			}
		}

		marketState, _ := market.GetMarketState(marketData)

		isAltcoin := coin.Symbol != "BTCUSDT" && coin.Symbol != "ETHUSDT"
		atr14 := 0.0
		if marketData.LongerTermContext != nil {
			atr14 = marketData.LongerTermContext.ATR14
		}
		suggestedSize, stopDist := market.CalculateAdaptivePositionSize(
			ctx.Account.TotalEquity,
			atr14,
			marketData.CurrentPrice,
			ctx.MaxRiskPerTrade,
			isAltcoin,
		)

		sb.WriteString(fmt.Sprintf("### %d. %s\n", displayedCount, coin.Symbol))
		sb.WriteString(fmt.Sprintf("**趋势**: %s%s\n", marketState, corrInfo))
		sb.WriteString(fmt.Sprintf("**建议仓位**: %.0f USD | **止损距离**: %.4f\n",
			suggestedSize, stopDist))
		sb.WriteString(market.FormatCompact(marketData))
		sb.WriteString("\n")

		if displayedCount >= 5 {
			break
		}
	}

	// 绩效指标
	stats := GetReturnsStats()
	if stats["count"] >= 5 {
		sb.WriteString("## 📈 绩效指标\n")
		sb.WriteString(fmt.Sprintf("**交易数**: %.0f | **胜率**: %.1f%%\n",
			stats["count"], stats["win_rate"]*100))
		sb.WriteString(fmt.Sprintf("**夏普比率**: %.2f | **索提诺比率**: %.2f\n\n",
			stats["sharpe_ratio"], stats["sortino_ratio"]))

		if stats["sharpe_ratio"] < -0.5 {
			sb.WriteString("⚠️ **夏普<-0.5**: 极其保守，只做置信度≥90的交易\n\n")
		} else if stats["sharpe_ratio"] < 0 {
			sb.WriteString("⚠️ **夏普<0**: 保守策略，只做置信度≥85的交易\n\n")
		}
	}

	sb.WriteString("---\n")
	sb.WriteString("请分析并输出开仓决策（简短分析 + JSON）\n")

	return sb.String()
}

// ============================================================================
// 交易执行回调
// ============================================================================

// OnPositionOpened 开仓成功后调用
func OnPositionOpened(decision *Decision, actualEntryPrice float64) {
	plan := CreateTradePlanFromDecision(decision, actualEntryPrice)
	log.Printf("✅ 开仓成功，交易计划已创建: %s %s @ %.4f",
		plan.Symbol, plan.Direction, actualEntryPrice)
}

// OnPositionClosed 平仓成功后调用（更新夏普比率）
func OnPositionClosed(symbol string, reason string, pnlPercent float64, holdTimeMinutes float64) {
	planManager.RemovePlan(symbol)

	// 记录收益率用于夏普比率计算
	AddReturn(pnlPercent)

	// 更新统计
	UpdateStatistics(pnlPercent, holdTimeMinutes)

	log.Printf("✅ 平仓成功: %s (原因: %s, 盈亏: %.2f%%, 持仓: %.0f分钟)",
		symbol, reason, pnlPercent, holdTimeMinutes)
}

// OnPositionClosedSimple 简化版平仓回调（向后兼容）
func OnPositionClosedSimple(symbol string, reason string) {
	planManager.RemovePlan(symbol)
	log.Printf("✅ 平仓成功，交易计划已移除: %s (原因: %s)", symbol, reason)
}

// OnPartialClose 部分平仓成功后调用
func OnPartialClose(symbol string, percentage float64) {
	plan := planManager.GetPlan(symbol)
	if plan == nil {
		return
	}

	if percentage >= 50 && !plan.PartialCloseAt1R3 {
		plan.PartialCloseAt1R3 = true
		log.Printf("✅ %s 部分平仓50%% @ RR 1:3", symbol)
	} else if percentage >= 30 && plan.PartialCloseAt1R3 && !plan.PartialCloseAt1R5 {
		plan.PartialCloseAt1R5 = true
		log.Printf("✅ %s 部分平仓30%% @ RR 1:5", symbol)
	}

	planManager.autoSaveIfEnabled()
}

// OnStopLossUpdated 止损更新成功后调用
func OnStopLossUpdated(symbol string, newStopLoss float64) {
	planManager.UpdatePlanStopLoss(symbol, newStopLoss)
	log.Printf("✅ %s 止损已更新至 %.4f", symbol, newStopLoss)
}

// ============================================================================
// 计划同步
// ============================================================================

// SyncPlansFromPositions 从现有持仓同步计划
// SyncPlansFromPositions 从现有持仓同步计划
func SyncPlansFromPositions(positions []PositionInfo, marketDataMap map[string]*market.Data) {
	for _, pos := range positions {
		if planManager.GetPlan(pos.Symbol) != nil {
			continue
		}

		marketData, ok := marketDataMap[pos.Symbol]
		if !ok {
			log.Printf("⚠️ 无法为 %s 创建恢复计划：缺少市场数据", pos.Symbol)
			continue
		}

		atr := 0.0
		if marketData.LongerTermContext != nil {
			atr = marketData.LongerTermContext.ATR14
		}

		isAltcoin := pos.Symbol != "BTCUSDT" && pos.Symbol != "ETHUSDT"
		multiplier := 1.8
		if isAltcoin {
			multiplier = 2.5
		}
		stopDistance := atr * multiplier

		var stopLoss, takeProfit float64
		if pos.Side == "long" {
			stopLoss = pos.EntryPrice - stopDistance
			takeProfit = pos.EntryPrice + stopDistance*3.5
		} else {
			stopLoss = pos.EntryPrice + stopDistance
			takeProfit = pos.EntryPrice - stopDistance*3.5
		}

		if pos.StopLoss > 0 {
			stopLoss = pos.StopLoss
		}
		if pos.TakeProfit > 0 {
			takeProfit = pos.TakeProfit
		}

		plan := &TradePlan{
			ID:              fmt.Sprintf("recovered_%s_%d", pos.Symbol, time.Now().UnixNano()),
			Symbol:          pos.Symbol,
			Direction:       pos.Side,
			EntryPrice:      pos.EntryPrice,
			StopLoss:        stopLoss,
			TakeProfit:      takeProfit,
			CurrentStopLoss: stopLoss,
			PositionSizeUSD: pos.MarginUsed * float64(pos.Leverage),
			Leverage:        pos.Leverage,
			EntryReason:     "从现有持仓恢复",
			MinHoldMinutes:  0,
			CreatedAt:       time.UnixMilli(pos.UpdateTime),
			Status:          "ACTIVE",
		}

		planManager.SetPlan(plan)
		log.Printf("📋 从持仓恢复交易计划: %s %s @ %.4f, SL=%.4f, TP=%.4f",
			pos.Symbol, pos.Side, pos.EntryPrice, stopLoss, takeProfit)
	}
}

// ============================================================================
// 计划状态查询
// ============================================================================

// GetAllPlans 获取所有活跃计划
func GetAllPlans() []*TradePlan {
	planManager.mu.RLock()
	defer planManager.mu.RUnlock()

	var plans []*TradePlan
	for _, plan := range planManager.plans {
		plans = append(plans, plan)
	}
	return plans
}

// GetPlanStatus 获取计划状态摘要
func GetPlanStatus() string {
	plans := GetAllPlans()
	if len(plans) == 0 {
		return "无活跃交易计划"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("活跃计划: %d个\n", len(plans)))
	for _, plan := range plans {
		holdingTime := time.Since(plan.CreatedAt).Minutes()
		sb.WriteString(fmt.Sprintf("  - %s %s: 持仓%.0f分钟, SL=%.4f, TP=%.4f\n",
			plan.Symbol, plan.Direction, holdingTime, plan.CurrentStopLoss, plan.TakeProfit))
	}
	return sb.String()
}

// GetPlanBySymbol 根据symbol获取计划
func GetPlanBySymbol(symbol string) *TradePlan {
	return planManager.GetPlan(symbol)
}

// ============================================================================
// 性能统计
// ============================================================================

// TradeStatistics 交易统计
type TradeStatistics struct {
	TotalTrades     int       `json:"total_trades"`
	WinningTrades   int       `json:"winning_trades"`
	LosingTrades    int       `json:"losing_trades"`
	TotalPnL        float64   `json:"total_pnl"`
	AverageWin      float64   `json:"average_win"`
	AverageLoss     float64   `json:"average_loss"`
	WinRate         float64   `json:"win_rate"`
	ProfitFactor    float64   `json:"profit_factor"`
	SharpeRatio     float64   `json:"sharpe_ratio"`
	SortinoRatio    float64   `json:"sortino_ratio"`
	MaxDrawdown     float64   `json:"max_drawdown"`
	AverageHoldTime float64   `json:"average_hold_time_minutes"`
	LastUpdated     time.Time `json:"last_updated"`
}

var (
	tradeStats     = &TradeStatistics{}
	tradeStatsLock sync.RWMutex
)

// UpdateStatistics 更新统计（同时更新夏普比率）
func UpdateStatistics(pnlPercent float64, holdTimeMinutes float64) {
	tradeStatsLock.Lock()
	defer tradeStatsLock.Unlock()

	tradeStats.TotalTrades++
	tradeStats.TotalPnL += pnlPercent

	if pnlPercent > 0 {
		tradeStats.WinningTrades++
		tradeStats.AverageWin = (tradeStats.AverageWin*float64(tradeStats.WinningTrades-1) + pnlPercent) / float64(tradeStats.WinningTrades)
	} else {
		tradeStats.LosingTrades++
		tradeStats.AverageLoss = (tradeStats.AverageLoss*float64(tradeStats.LosingTrades-1) + math.Abs(pnlPercent)) / float64(tradeStats.LosingTrades)
	}

	if tradeStats.TotalTrades > 0 {
		tradeStats.WinRate = float64(tradeStats.WinningTrades) / float64(tradeStats.TotalTrades)
	}

	if tradeStats.AverageLoss > 0 && tradeStats.WinRate < 1 {
		tradeStats.ProfitFactor = (tradeStats.AverageWin * tradeStats.WinRate) / (tradeStats.AverageLoss * (1 - tradeStats.WinRate))
	}

	tradeStats.AverageHoldTime = (tradeStats.AverageHoldTime*float64(tradeStats.TotalTrades-1) + holdTimeMinutes) / float64(tradeStats.TotalTrades)
	tradeStats.LastUpdated = time.Now()

	// 更新夏普比率和索提诺比率
	tradeStats.SharpeRatio = CalculateSharpeRatio()
	tradeStats.SortinoRatio = CalculateSortinoRatio()

	log.Printf("📊 统计更新: 总交易=%d, 胜率=%.1f%%, 盈亏因子=%.2f, 夏普=%.2f",
		tradeStats.TotalTrades, tradeStats.WinRate*100, tradeStats.ProfitFactor, tradeStats.SharpeRatio)

	// 自动保存
	if planManager != nil {
		planManager.autoSaveIfEnabled()
	}
}

// GetStatistics 获取统计信息
func GetStatistics() *TradeStatistics {
	tradeStatsLock.RLock()
	defer tradeStatsLock.RUnlock()
	statsCopy := *tradeStats
	return &statsCopy
}

// ResetStatistics 重置统计
func ResetStatistics() {
	tradeStatsLock.Lock()
	defer tradeStatsLock.Unlock()
	tradeStats = &TradeStatistics{}

	returnsLock.Lock()
	returnsSeries = nil
	returnsLock.Unlock()

	log.Printf("📊 统计已重置")

	if planManager != nil {
		planManager.autoSaveIfEnabled()
	}
}

// ============================================================================
// 导出函数（供外部调用）
// ============================================================================

// CalculateOptimalPosition 计算最优仓位
func CalculateOptimalPosition(ctx *Context, symbol string, side string) (positionSize, stopLoss, takeProfit float64, err error) {
	marketData, ok := ctx.MarketDataMap[symbol]
	if !ok {
		return 0, 0, 0, fmt.Errorf("缺少 %s 市场数据", symbol)
	}

	currentPrice := marketData.CurrentPrice
	atr14 := 0.0
	if marketData.LongerTermContext != nil {
		atr14 = marketData.LongerTermContext.ATR14
	}

	isAltcoin := symbol != "BTCUSDT" && symbol != "ETHUSDT"

	suggestedSize, stopDistance := market.CalculateAdaptivePositionSize(
		ctx.Account.TotalEquity,
		atr14,
		currentPrice,
		ctx.MaxRiskPerTrade,
		isAltcoin,
	)

	if corr, ok := ctx.CorrelationMap[symbol]; ok {
		suggestedSize *= corr.RiskWeight
	}

	if side == "long" {
		stopLoss = currentPrice - stopDistance
		takeProfit = currentPrice + stopDistance*3.5
	} else {
		stopLoss = currentPrice + stopDistance
		takeProfit = currentPrice - stopDistance*3.5
	}

	maxLeverage := ctx.AltcoinLeverage
	if !isAltcoin {
		maxLeverage = ctx.BTCETHLeverage
	}
	maxPositionValue := ctx.Account.AvailableBalance * float64(maxLeverage) * 0.9

	if suggestedSize > maxPositionValue {
		suggestedSize = maxPositionValue
	}

	return suggestedSize, stopLoss, takeProfit, nil
}

// GetTradingRecommendation 获取交易建议
func GetTradingRecommendation(ctx *Context, symbol string) string {
	marketData, ok := ctx.MarketDataMap[symbol]
	if !ok {
		return "缺少市场数据"
	}

	var recommendations []string

	state, confidence := market.GetMarketState(marketData)
	recommendations = append(recommendations, fmt.Sprintf("市场状态: %s (置信度%d%%)", state, confidence))

	if marketData.LongerTermContext != nil {
		if marketData.LongerTermContext.EMA20 > marketData.LongerTermContext.EMA50 {
			recommendations = append(recommendations, "4小时EMA: 多头排列")
		} else {
			recommendations = append(recommendations, "4小时EMA: 空头排列")
		}
	}

	fundingPct := marketData.FundingRate * 100
	if fundingPct > 0.05 {
		recommendations = append(recommendations, fmt.Sprintf("资金费率: %.4f%% (做空有利)", fundingPct))
	} else if fundingPct < -0.05 {
		recommendations = append(recommendations, fmt.Sprintf("资金费率: %.4f%% (做多有利)", fundingPct))
	}

	if corr, ok := ctx.CorrelationMap[symbol]; ok {
		if corr.IsHighCorr {
			recommendations = append(recommendations, fmt.Sprintf("BTC相关性: %.2f (高，需降低仓位)", corr.BTCCorr))
		}
	}

	return strings.Join(recommendations, " | ")
}

// ============================================================================
// 决策执行器（供主程序调用）
// ============================================================================

// DecisionExecutor 执行决策的接口定义
type DecisionExecutor interface {
	OpenPosition(symbol, side string, leverage int, sizeUSD, stopLoss, takeProfit float64) error
	ClosePosition(symbol string, percentage float64) error
	UpdateStopLoss(symbol string, newStopLoss float64) error
}

// ProcessDecisions 处理决策列表
func ProcessDecisions(decisions []Decision, executor DecisionExecutor) error {
	for _, d := range decisions {
		var err error

		switch d.Action {
		case "open_long":
			err = executor.OpenPosition(d.Symbol, "long", d.Leverage, d.PositionSizeUSD, d.StopLoss, d.TakeProfit)
			if err == nil {
				OnPositionOpened(&d, 0)
			}

		case "open_short":
			err = executor.OpenPosition(d.Symbol, "short", d.Leverage, d.PositionSizeUSD, d.StopLoss, d.TakeProfit)
			if err == nil {
				OnPositionOpened(&d, 0)
			}

		case "close_long", "close_short":
			err = executor.ClosePosition(d.Symbol, 100)
			if err == nil {
				OnPositionClosedSimple(d.Symbol, d.Reasoning)
			}

		case "partial_close":
			err = executor.ClosePosition(d.Symbol, d.ClosePercentage)
			if err == nil {
				OnPartialClose(d.Symbol, d.ClosePercentage)
			}

		case "update_stop_loss":
			err = executor.UpdateStopLoss(d.Symbol, d.NewStopLoss)
			if err == nil {
				OnStopLossUpdated(d.Symbol, d.NewStopLoss)
			}

		case "hold", "wait":
			continue
		}

		if err != nil {
			log.Printf("❌ 执行 %s %s 失败: %v", d.Symbol, d.Action, err)
		}
	}

	return nil
}

// ============================================================================
// 调试和监控
// ============================================================================

// DebugContext 输出调试信息
func DebugContext(ctx *Context) string {
	var sb strings.Builder

	sb.WriteString("=== 决策系统状态 ===\n")
	sb.WriteString(fmt.Sprintf("时间: %s\n", ctx.CurrentTime))
	sb.WriteString(fmt.Sprintf("运行时间: %d分钟\n", ctx.RuntimeMinutes))
	sb.WriteString(fmt.Sprintf("调用次数: %d\n", ctx.CallCount))
	sb.WriteString(fmt.Sprintf("上次分析: %s\n", ctx.LastAnalysisTime.Format("15:04:05")))
	sb.WriteString(fmt.Sprintf("分析间隔: %d分钟\n\n", ctx.AnalysisIntervalMin))

	sb.WriteString("=== 账户状态 ===\n")
	sb.WriteString(fmt.Sprintf("净值: %.2f USDT\n", ctx.Account.TotalEquity))
	sb.WriteString(fmt.Sprintf("可用: %.2f USDT\n", ctx.Account.AvailableBalance))
	sb.WriteString(fmt.Sprintf("保证金使用率: %.1f%%\n", ctx.Account.MarginUsedPct))
	sb.WriteString(fmt.Sprintf("持仓数: %d\n\n", ctx.Account.PositionCount))

	sb.WriteString("=== 风险预算 ===\n")
	usedRisk := calculateUsedRisk(ctx)
	remainingRisk := ctx.TotalRiskBudget - usedRisk
	sb.WriteString(fmt.Sprintf("总预算: %.1f%%\n", ctx.TotalRiskBudget*100))
	sb.WriteString(fmt.Sprintf("已用: %.1f%%\n", usedRisk*100))
	sb.WriteString(fmt.Sprintf("剩余: %.1f%%\n\n", remainingRisk*100))

	sb.WriteString("=== 绩效指标 ===\n")
	stats := GetStatistics()
	sb.WriteString(fmt.Sprintf("总交易: %d | 胜率: %.1f%%\n", stats.TotalTrades, stats.WinRate*100))
	sb.WriteString(fmt.Sprintf("夏普比率: %.2f | 索提诺比率: %.2f\n", stats.SharpeRatio, stats.SortinoRatio))
	sb.WriteString(fmt.Sprintf("盈亏因子: %.2f | 平均持仓: %.0f分钟\n\n", stats.ProfitFactor, stats.AverageHoldTime))

	sb.WriteString("=== 交易计划 ===\n")
	sb.WriteString(GetPlanStatus())

	if ctx.CircuitBreaker != nil && ctx.CircuitBreaker.IsTriggered {
		sb.WriteString("\n=== 熔断状态 ===\n")
		sb.WriteString(fmt.Sprintf("触发原因: %s\n", ctx.CircuitBreaker.TriggerReason))
		sb.WriteString(fmt.Sprintf("触发时间: %s\n", ctx.CircuitBreaker.TriggerTime.Format("15:04:05")))
		sb.WriteString(fmt.Sprintf("冷却时间: %d分钟\n", ctx.CircuitBreaker.CooldownMinutes))
	}

	return sb.String()
}

// ============================================================================
// 快捷函数
// ============================================================================

// QuickAnalyze 快速分析（不调用AI）
func QuickAnalyze(ctx *Context) string {
	var sb strings.Builder

	sb.WriteString("=== 快速市场分析 ===\n\n")

	if btcData, ok := ctx.MarketDataMap["BTCUSDT"]; ok {
		state, conf := market.GetMarketState(btcData)
		sb.WriteString(fmt.Sprintf("**BTC**: %.2f | %s (%d%%)\n", btcData.CurrentPrice, state, conf))
		sb.WriteString(fmt.Sprintf("  ADX=%.1f | DI+=%.1f | DI-=%.1f | RSI=%.1f\n\n",
			btcData.CurrentADX, btcData.CurrentDIPlus, btcData.CurrentDIMinus, btcData.CurrentRSI14))
	}

	if len(ctx.Positions) > 0 {
		sb.WriteString("**持仓状态**:\n")
		for _, pos := range ctx.Positions {
			plan := planManager.GetPlan(pos.Symbol)
			planInfo := "无计划"
			if plan != nil {
				holdMin := time.Since(plan.CreatedAt).Minutes()
				planInfo = fmt.Sprintf("持仓%.0f分钟, SL=%.4f", holdMin, plan.CurrentStopLoss)
			}
			sb.WriteString(fmt.Sprintf("  %s %s: %+.2f%% | %s\n",
				pos.Symbol, pos.Side, pos.UnrealizedPnLPct, planInfo))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("**候选币评分**:\n")
	for _, coin := range ctx.CandidateCoins {
		if coin.Symbol == "BTCUSDT" {
			continue
		}
		if data, ok := ctx.MarketDataMap[coin.Symbol]; ok {
			state, conf := market.GetMarketState(data)
			score := calculateCoinScore(data, ctx.CorrelationMap[coin.Symbol])
			sb.WriteString(fmt.Sprintf("  %s: %s(%d%%) | 评分=%.1f\n",
				coin.Symbol, state, conf, score))
		}
	}

	return sb.String()
}

// calculateCoinScore 计算币种评分
func calculateCoinScore(data *market.Data, corr *CorrelationData) float64 {
	score := 50.0

	if data.CurrentADX > 25 {
		score += 15
	} else if data.CurrentADX > 20 {
		score += 8
	}

	if data.CurrentRSI14 > 30 && data.CurrentRSI14 < 70 {
		score += 10
	}

	if data.LongerTermContext != nil {
		pos := data.LongerTermContext.PricePosition
		if pos > 0.2 && pos < 0.8 {
			score += 10
		}
	}

	if corr != nil && corr.IsHighCorr {
		score -= 10
	}

	oiMil := data.OIValueUSD / 1_000_000
	if oiMil > 50 {
		score += 15
	} else if oiMil > 30 {
		score += 10
	} else if oiMil > 15 {
		score += 5
	}

	return score
}

// ============================================================================
// 初始化函数
// ============================================================================

// Config 配置结构
type Config struct {
	MaxRiskPerTrade     float64 `json:"max_risk_per_trade"`
	TotalRiskBudget     float64 `json:"total_risk_budget"`
	AnalysisIntervalMin int     `json:"analysis_interval_min"`
	BTCETHLeverage      int     `json:"btc_eth_leverage"`
	AltcoinLeverage     int     `json:"altcoin_leverage"`
	DataDir             string  `json:"data_dir"`
	RiskFreeRate        float64 `json:"risk_free_rate"`
}

// Initialize 初始化决策模块
func Initialize(config *Config) error {
	if config == nil {
		config = &Config{
			MaxRiskPerTrade:     0.02,
			TotalRiskBudget:     0.08,
			AnalysisIntervalMin: 15,
			BTCETHLeverage:      10,
			AltcoinLeverage:     5,
			DataDir:             defaultDataDir,
			RiskFreeRate:        0.0,
		}
	}

	// 初始化计划管理器（带持久化）
	if err := InitPlanManager(config.DataDir); err != nil {
		return fmt.Errorf("初始化计划管理器失败: %w", err)
	}

	// 设置夏普比率配置
	SetSharpeConfig(SharpeConfig{
		RiskFreeRate:     config.RiskFreeRate,
		AnnualizeFactor:  252,
		MinTradesForCalc: 10,
	})

	log.Printf("📊 决策模块初始化: 单笔风险=%.1f%%, 总预算=%.1f%%, 分析间隔=%d分钟, 数据目录=%s",
		config.MaxRiskPerTrade*100, config.TotalRiskBudget*100, config.AnalysisIntervalMin, config.DataDir)

	// 输出当前统计
	stats := GetStatistics()
	if stats.TotalTrades > 0 {
		log.Printf("📊 恢复历史统计: 总交易=%d, 胜率=%.1f%%, 夏普=%.2f",
			stats.TotalTrades, stats.WinRate*100, stats.SharpeRatio)
	}

	return nil
}

// Shutdown 关闭决策模块（确保数据保存）
func Shutdown() error {
	if planManager != nil {
		if err := planManager.ForceSave(); err != nil {
			return fmt.Errorf("保存数据失败: %w", err)
		}
		log.Printf("📂 决策模块数据已保存")
	}
	return nil
}

// ============================================================================
// 额外工具函数
// ============================================================================

// GetPerformanceReport 获取完整绩效报告
func GetPerformanceReport() string {
	var sb strings.Builder

	stats := GetStatistics()
	returnsStats := GetReturnsStats()

	sb.WriteString("═══════════════════════════════════════\n")
	sb.WriteString("           📊 绩效报告                  \n")
	sb.WriteString("═══════════════════════════════════════\n\n")

	sb.WriteString("【交易统计】\n")
	sb.WriteString(fmt.Sprintf("  总交易数: %d\n", stats.TotalTrades))
	sb.WriteString(fmt.Sprintf("  盈利交易: %d\n", stats.WinningTrades))
	sb.WriteString(fmt.Sprintf("  亏损交易: %d\n", stats.LosingTrades))
	sb.WriteString(fmt.Sprintf("  胜率: %.2f%%\n\n", stats.WinRate*100))

	sb.WriteString("【盈亏分析】\n")
	sb.WriteString(fmt.Sprintf("  总盈亏: %.2f%%\n", stats.TotalPnL))
	sb.WriteString(fmt.Sprintf("  平均盈利: %.2f%%\n", stats.AverageWin))
	sb.WriteString(fmt.Sprintf("  平均亏损: %.2f%%\n", stats.AverageLoss))
	sb.WriteString(fmt.Sprintf("  盈亏因子: %.2f\n\n", stats.ProfitFactor))

	sb.WriteString("【风险调整收益】\n")
	sb.WriteString(fmt.Sprintf("  夏普比率: %.2f\n", returnsStats["sharpe_ratio"]))
	sb.WriteString(fmt.Sprintf("  索提诺比率: %.2f\n", returnsStats["sortino_ratio"]))
	sb.WriteString(fmt.Sprintf("  最大回撤: %.2f%%\n\n", stats.MaxDrawdown))

	sb.WriteString("【其他指标】\n")
	sb.WriteString(fmt.Sprintf("  平均持仓时间: %.0f 分钟\n", stats.AverageHoldTime))
	sb.WriteString(fmt.Sprintf("  最后更新: %s\n", stats.LastUpdated.Format("2006-01-02 15:04:05")))

	sb.WriteString("\n═══════════════════════════════════════\n")

	// 夏普比率解读
	sharpe := returnsStats["sharpe_ratio"]
	sb.WriteString("\n【夏普比率解读】\n")
	if sharpe > 2.0 {
		sb.WriteString("  ✅ 优秀 (>2.0): 策略表现非常好\n")
	} else if sharpe > 1.0 {
		sb.WriteString("  ✅ 良好 (1.0-2.0): 策略表现不错\n")
	} else if sharpe > 0 {
		sb.WriteString("  ⚠️ 一般 (0-1.0): 策略有改进空间\n")
	} else {
		sb.WriteString("  ❌ 较差 (<0): 策略需要调整\n")
	}

	return sb.String()
}

// ExportData 导出所有数据为JSON
func ExportData() ([]byte, error) {
	planManager.mu.RLock()
	plansCopy := make(map[string]*TradePlan)
	for k, v := range planManager.plans {
		plansCopy[k] = v
	}
	planManager.mu.RUnlock()

	tradeStatsLock.RLock()
	statsCopy := *tradeStats
	tradeStatsLock.RUnlock()

	returnsLock.RLock()
	returnsCopy := make([]float64, len(returnsSeries))
	copy(returnsCopy, returnsSeries)
	returnsLock.RUnlock()

	exportData := struct {
		Plans        map[string]*TradePlan `json:"plans"`
		Statistics   *TradeStatistics      `json:"statistics"`
		Returns      []float64             `json:"returns"`
		ReturnsStats map[string]float64    `json:"returns_stats"`
		ExportedAt   time.Time             `json:"exported_at"`
	}{
		Plans:        plansCopy,
		Statistics:   &statsCopy,
		Returns:      returnsCopy,
		ReturnsStats: GetReturnsStats(),
		ExportedAt:   time.Now(),
	}

	return json.MarshalIndent(exportData, "", "  ")
}

// ImportData 导入数据
func ImportData(data []byte) error {
	var importData struct {
		Plans      map[string]*TradePlan `json:"plans"`
		Statistics *TradeStatistics      `json:"statistics"`
		Returns    []float64             `json:"returns"`
	}

	if err := json.Unmarshal(data, &importData); err != nil {
		return fmt.Errorf("解析导入数据失败: %w", err)
	}

	if importData.Plans != nil {
		planManager.mu.Lock()
		planManager.plans = importData.Plans
		planManager.mu.Unlock()
	}

	if importData.Statistics != nil {
		tradeStatsLock.Lock()
		tradeStats = importData.Statistics
		tradeStatsLock.Unlock()
	}

	if importData.Returns != nil {
		returnsLock.Lock()
		returnsSeries = importData.Returns
		returnsLock.Unlock()
	}

	// 保存到文件
	if err := planManager.ForceSave(); err != nil {
		return fmt.Errorf("保存导入数据失败: %w", err)
	}

	log.Printf("📂 成功导入数据: %d个计划, %d笔交易记录",
		len(importData.Plans), len(importData.Returns))

	return nil
}
