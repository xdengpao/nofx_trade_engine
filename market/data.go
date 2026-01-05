package market

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Data 市场数据结构（优化版）
type Data struct {
	Symbol            string
	CurrentPrice      float64
	PriceChange1h     float64 // 1小时价格变化百分比
	PriceChange4h     float64 // 4小时价格变化百分比
	CurrentEMA20      float64
	CurrentEMA50      float64 // 新增：EMA50
	CurrentMACD       float64
	CurrentRSI7       float64
	CurrentRSI14      float64 // 新增：RSI14
	CurrentADX        float64 // 新增：ADX趋势强度
	CurrentDIPlus     float64 // 新增：DI+
	CurrentDIMinus    float64 // 新增：DI-
	BollingerWidth    float64 // 新增：布林带宽度（波动率指标）
	OpenInterest      *OIData
	OIValueUSD        float64 // 新增：OI价值（USD）
	FundingRate       float64
	IntradaySeries    *IntradayData   // 3分钟数据 - 实时价格
	MidTermSeries15m  *MidTermData15m // 15分钟数据 - 短期趋势
	MidTermSeries1h   *MidTermData1h  // 1小时数据 - 中期趋势
	LongerTermContext *LongerTermData // 4小时数据 - 长期趋势
}

// OIData Open Interest数据（优化版）
type OIData struct {
	Latest    float64
	Average   float64
	Change1h  float64   // 新增：1小时OI变化百分比
	Change4h  float64   // 新增：4小时OI变化百分比
	HistoryOI []float64 // 新增：历史OI序列（用于趋势分析）
}

// IntradayData 日内数据(3分钟间隔) - 主要用于获取实时价格
type IntradayData struct {
	MidPrices   []float64
	HighPrices  []float64 // 新增：最高价序列
	LowPrices   []float64 // 新增：最低价序列
	Volumes     []float64 // 新增：成交量序列
	EMA20Values []float64
	EMA50Values []float64 // 新增：EMA50序列
	MACDValues  []float64
	MACDSignal  []float64 // 新增：MACD信号线
	MACDHist    []float64 // 新增：MACD柱状图
	RSI7Values  []float64
	RSI14Values []float64
	ATRValues   []float64 // 新增：ATR序列
}

// MidTermData15m 15分钟时间框架数据 - 短期趋势过滤
type MidTermData15m struct {
	MidPrices   []float64
	HighPrices  []float64
	LowPrices   []float64
	Volumes     []float64
	EMA20Values []float64
	EMA50Values []float64
	MACDValues  []float64
	MACDSignal  []float64
	MACDHist    []float64
	RSI7Values  []float64
	RSI14Values []float64
	ADXValues   []float64 // 新增：ADX序列
	ATRValues   []float64
}

// MidTermData1h 1小时时间框架数据 - 中期趋势确认
type MidTermData1h struct {
	MidPrices   []float64
	HighPrices  []float64
	LowPrices   []float64
	Volumes     []float64
	EMA20Values []float64
	EMA50Values []float64
	MACDValues  []float64
	MACDSignal  []float64
	MACDHist    []float64
	RSI7Values  []float64
	RSI14Values []float64
	ADXValues   []float64
	ATRValues   []float64
}

// LongerTermData 长期数据(4小时时间框架)（优化版）
type LongerTermData struct {
	EMA20          float64
	EMA50          float64
	ATR3           float64
	ATR14          float64
	ATRRatio       float64 // 新增：ATR3/ATR14比值（波动率变化）
	CurrentVolume  float64
	AverageVolume  float64
	VolumeRatio    float64 // 新增：当前成交量/平均成交量
	MACDValues     []float64
	MACDSignal     []float64 // 新增：MACD信号线序列
	MACDHist       []float64 // 新增：MACD柱状图序列
	RSI14Values    []float64
	ADXValues      []float64 // 新增：ADX序列
	DIPlus         []float64 // 新增：DI+序列
	DIMinus        []float64 // 新增：DI-序列
	BollingerUpper float64   // 新增：布林带上轨
	BollingerLower float64   // 新增：布林带下轨
	BollingerWidth float64   // 新增：布林带宽度
	PricePosition  float64   // 新增：价格在布林带中的位置（0-1）
}

// Kline K线数据
type Kline struct {
	OpenTime  int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	CloseTime int64
}

// 缓存结构（避免重复请求）
var (
	dataCache     = make(map[string]*cacheEntry)
	dataCacheLock sync.RWMutex
)

type cacheEntry struct {
	data      *Data
	timestamp time.Time
}

const cacheDuration = 30 * time.Second // 缓存30秒

// Get 获取指定代币的市场数据（优化版：带缓存）
func Get(symbol string) (*Data, error) {
	// 标准化symbol
	symbol = Normalize(symbol)

	// 检查缓存
	dataCacheLock.RLock()
	if entry, ok := dataCache[symbol]; ok {
		if time.Since(entry.timestamp) < cacheDuration {
			dataCacheLock.RUnlock()
			return entry.data, nil
		}
	}
	dataCacheLock.RUnlock()

	// 并发获取K线数据
	var wg sync.WaitGroup
	var klines3m, klines15m, klines1h, klines4h []Kline
	var err3m, err15m, err1h, err4h error

	wg.Add(4)
	go func() {
		defer wg.Done()
		klines3m, err3m = getKlines(symbol, "3m", 50)
	}()
	go func() {
		defer wg.Done()
		klines15m, err15m = getKlines(symbol, "15m", 60)
	}()
	go func() {
		defer wg.Done()
		klines1h, err1h = getKlines(symbol, "1h", 80)
	}()
	go func() {
		defer wg.Done()
		klines4h, err4h = getKlines(symbol, "4h", 80)
	}()
	wg.Wait()

	// 检查错误
	if err3m != nil {
		return nil, fmt.Errorf("获取3分钟K线失败: %v", err3m)
	}
	if err15m != nil {
		return nil, fmt.Errorf("获取15分钟K线失败: %v", err15m)
	}
	if err1h != nil {
		return nil, fmt.Errorf("获取1小时K线失败: %v", err1h)
	}
	if err4h != nil {
		return nil, fmt.Errorf("获取4小时K线失败: %v", err4h)
	}

	// 计算当前指标 (基于3分钟最新数据)
	currentPrice := klines3m[len(klines3m)-1].Close
	currentEMA20 := calculateEMA(klines3m, 20)
	currentEMA50 := calculateEMA(klines3m, 50)
	macdLine, macdSignal, _ := calculateMACDFull(klines3m)
	currentMACD := macdLine
	currentRSI7 := calculateRSI(klines3m, 7)
	currentRSI14 := calculateRSI(klines3m, 14)

	// 计算ADX和DI（基于4小时数据）
	currentADX, currentDIPlus, currentDIMinus := calculateADX(klines4h, 14)

	// 计算布林带宽度
	bollingerUpper, bollingerLower, bollingerWidth := calculateBollingerBands(klines4h, 20, 2.0)
	_ = bollingerUpper
	_ = bollingerLower

	// 计算价格变化百分比
	priceChange1h := 0.0
	if len(klines3m) >= 21 {
		price1hAgo := klines3m[len(klines3m)-21].Close
		if price1hAgo > 0 {
			priceChange1h = ((currentPrice - price1hAgo) / price1hAgo) * 100
		}
	}

	priceChange4h := 0.0
	if len(klines4h) >= 2 {
		price4hAgo := klines4h[len(klines4h)-2].Close
		if price4hAgo > 0 {
			priceChange4h = ((currentPrice - price4hAgo) / price4hAgo) * 100
		}
	}

	// 获取OI数据（优化版）
	oiData, err := getOpenInterestDataEnhanced(symbol)
	if err != nil {
		oiData = &OIData{Latest: 0, Average: 0}
	}

	// 计算OI价值（USD）
	oiValueUSD := oiData.Latest * currentPrice

	// 获取Funding Rate
	fundingRate, _ := getFundingRate(symbol)

	// 计算各时间框架数据
	intradayData := calculateIntradaySeriesEnhanced(klines3m)
	midTermData15m := calculateMidTermSeries15mEnhanced(klines15m)
	midTermData1h := calculateMidTermSeries1hEnhanced(klines1h)
	longerTermData := calculateLongerTermDataEnhanced(klines4h)

	data := &Data{
		Symbol:            symbol,
		CurrentPrice:      currentPrice,
		PriceChange1h:     priceChange1h,
		PriceChange4h:     priceChange4h,
		CurrentEMA20:      currentEMA20,
		CurrentEMA50:      currentEMA50,
		CurrentMACD:       currentMACD,
		CurrentRSI7:       currentRSI7,
		CurrentRSI14:      currentRSI14,
		CurrentADX:        currentADX,
		CurrentDIPlus:     currentDIPlus,
		CurrentDIMinus:    currentDIMinus,
		BollingerWidth:    bollingerWidth,
		OpenInterest:      oiData,
		OIValueUSD:        oiValueUSD,
		FundingRate:       fundingRate,
		IntradaySeries:    intradayData,
		MidTermSeries15m:  midTermData15m,
		MidTermSeries1h:   midTermData1h,
		LongerTermContext: longerTermData,
	}

	// 更新缓存
	dataCacheLock.Lock()
	dataCache[symbol] = &cacheEntry{
		data:      data,
		timestamp: time.Now(),
	}
	dataCacheLock.Unlock()

	// 补充MACD信号线
	_ = macdSignal

	return data, nil
}

// getKlines 从Binance获取K线数据
func getKlines(symbol, interval string, limit int) ([]Kline, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/klines?symbol=%s&interval=%s&limit=%d",
		symbol, interval, limit)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var rawData [][]interface{}
	if err := json.Unmarshal(body, &rawData); err != nil {
		return nil, err
	}

	klines := make([]Kline, len(rawData))
	for i, item := range rawData {
		openTime := int64(item[0].(float64))
		open, _ := parseFloat(item[1])
		high, _ := parseFloat(item[2])
		low, _ := parseFloat(item[3])
		close, _ := parseFloat(item[4])
		volume, _ := parseFloat(item[5])
		closeTime := int64(item[6].(float64))

		klines[i] = Kline{
			OpenTime:  openTime,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     close,
			Volume:    volume,
			CloseTime: closeTime,
		}
	}

	return klines, nil
}

// calculateEMA 计算EMA
func calculateEMA(klines []Kline, period int) float64 {
	if len(klines) < period {
		return 0
	}

	sum := 0.0
	for i := 0; i < period; i++ {
		sum += klines[i].Close
	}
	ema := sum / float64(period)

	multiplier := 2.0 / float64(period+1)
	for i := period; i < len(klines); i++ {
		ema = (klines[i].Close-ema)*multiplier + ema
	}

	return ema
}

// calculateEMAFromSlice 从float64切片计算EMA
func calculateEMAFromSlice(prices []float64, period int) float64 {
	if len(prices) < period {
		return 0
	}

	sum := 0.0
	for i := 0; i < period; i++ {
		sum += prices[i]
	}
	ema := sum / float64(period)

	multiplier := 2.0 / float64(period+1)
	for i := period; i < len(prices); i++ {
		ema = (prices[i]-ema)*multiplier + ema
	}

	return ema
}

// calculateMACD 计算MACD
func calculateMACD(klines []Kline) float64 {
	if len(klines) < 26 {
		return 0
	}

	ema12 := calculateEMA(klines, 12)
	ema26 := calculateEMA(klines, 26)

	return ema12 - ema26
}

// calculateMACDFull 计算完整MACD（MACD线、信号线、柱状图）
func calculateMACDFull(klines []Kline) (macdLine, signalLine, histogram float64) {
	if len(klines) < 35 {
		return 0, 0, 0
	}

	// 计算MACD线序列
	macdValues := make([]float64, 0)
	for i := 26; i <= len(klines); i++ {
		ema12 := calculateEMA(klines[:i], 12)
		ema26 := calculateEMA(klines[:i], 26)
		macdValues = append(macdValues, ema12-ema26)
	}

	if len(macdValues) < 9 {
		return macdValues[len(macdValues)-1], 0, 0
	}

	// 计算信号线（MACD的9日EMA）
	signalLine = calculateEMAFromSlice(macdValues, 9)
	macdLine = macdValues[len(macdValues)-1]
	histogram = macdLine - signalLine

	return macdLine, signalLine, histogram
}

// calculateRSI 计算RSI
func calculateRSI(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	gains := 0.0
	losses := 0.0

	for i := 1; i <= period; i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses += -change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	for i := period + 1; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			avgGain = (avgGain*float64(period-1) + change) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-change)) / float64(period)
		}
	}

	if avgLoss == 0 {
		return 100
	}

	rs := avgGain / avgLoss
	rsi := 100 - (100 / (1 + rs))

	return rsi
}

// calculateATR 计算ATR
func calculateATR(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	trs := make([]float64, len(klines))
	for i := 1; i < len(klines); i++ {
		high := klines[i].High
		low := klines[i].Low
		prevClose := klines[i-1].Close

		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)

		trs[i] = math.Max(tr1, math.Max(tr2, tr3))
	}

	sum := 0.0
	for i := 1; i <= period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)

	for i := period + 1; i < len(klines); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}

	return atr
}

// calculateADX 计算ADX和方向指标（新增）
func calculateADX(klines []Kline, period int) (adx, diPlus, diMinus float64) {
	if len(klines) < period*2 {
		return 0, 0, 0
	}

	// 计算+DM, -DM, TR序列
	plusDM := make([]float64, len(klines))
	minusDM := make([]float64, len(klines))
	tr := make([]float64, len(klines))

	for i := 1; i < len(klines); i++ {
		high := klines[i].High
		low := klines[i].Low
		prevHigh := klines[i-1].High
		prevLow := klines[i-1].Low
		prevClose := klines[i-1].Close

		// True Range
		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)
		tr[i] = math.Max(tr1, math.Max(tr2, tr3))

		// +DM和-DM
		upMove := high - prevHigh
		downMove := prevLow - low

		if upMove > downMove && upMove > 0 {
			plusDM[i] = upMove
		}
		if downMove > upMove && downMove > 0 {
			minusDM[i] = downMove
		}
	}

	// 平滑计算（Wilder平滑）
	smoothedPlusDM := 0.0
	smoothedMinusDM := 0.0
	smoothedTR := 0.0

	// 初始值
	for i := 1; i <= period; i++ {
		smoothedPlusDM += plusDM[i]
		smoothedMinusDM += minusDM[i]
		smoothedTR += tr[i]
	}

	// Wilder平滑
	for i := period + 1; i < len(klines); i++ {
		smoothedPlusDM = smoothedPlusDM - (smoothedPlusDM / float64(period)) + plusDM[i]
		smoothedMinusDM = smoothedMinusDM - (smoothedMinusDM / float64(period)) + minusDM[i]
		smoothedTR = smoothedTR - (smoothedTR / float64(period)) + tr[i]
	}

	// 计算+DI和-DI
	if smoothedTR > 0 {
		diPlus = (smoothedPlusDM / smoothedTR) * 100
		diMinus = (smoothedMinusDM / smoothedTR) * 100
	}

	// 计算DX和ADX
	diSum := diPlus + diMinus
	if diSum > 0 {
		dx := math.Abs(diPlus-diMinus) / diSum * 100
		adx = dx // 简化：直接使用DX作为ADX（完整版需要再平滑一次）
	}

	return adx, diPlus, diMinus
}

// calculateBollingerBands 计算布林带（新增）
func calculateBollingerBands(klines []Kline, period int, stdDevMultiplier float64) (upper, lower, width float64) {
	if len(klines) < period {
		return 0, 0, 0
	}

	// 计算SMA
	sum := 0.0
	for i := len(klines) - period; i < len(klines); i++ {
		sum += klines[i].Close
	}
	sma := sum / float64(period)

	// 计算标准差
	variance := 0.0
	for i := len(klines) - period; i < len(klines); i++ {
		diff := klines[i].Close - sma
		variance += diff * diff
	}
	stdDev := math.Sqrt(variance / float64(period))

	upper = sma + stdDevMultiplier*stdDev
	lower = sma - stdDevMultiplier*stdDev
	width = (upper - lower) / sma * 100 // 宽度百分比

	return upper, lower, width
}

// getOpenInterestDataEnhanced 获取增强版OI数据
func getOpenInterestDataEnhanced(symbol string) (*OIData, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/openInterest?symbol=%s", symbol)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		OpenInterest string `json:"openInterest"`
		Symbol       string `json:"symbol"`
		Time         int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	oi, _ := strconv.ParseFloat(result.OpenInterest, 64)

	// 尝试获取历史OI数据
	historyOI, change1h, change4h := getOIHistory(symbol)

	return &OIData{
		Latest:    oi,
		Average:   oi * 0.999,
		Change1h:  change1h,
		Change4h:  change4h,
		HistoryOI: historyOI,
	}, nil
}

// getOIHistory 获取历史OI数据（用于计算变化率）
func getOIHistory(symbol string) (history []float64, change1h, change4h float64) {
	url := fmt.Sprintf("https://fapi.binance.com/futures/data/openInterestHist?symbol=%s&period=5m&limit=50", symbol)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, 0, 0
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, 0
	}

	var data []struct {
		SumOpenInterest string `json:"sumOpenInterest"`
		Timestamp       int64  `json:"timestamp"`
	}

	if err := json.Unmarshal(body, &data); err != nil {
		return nil, 0, 0
	}

	history = make([]float64, len(data))
	for i, d := range data {
		oi, _ := strconv.ParseFloat(d.SumOpenInterest, 64)
		history[i] = oi
	}

	// 计算变化率
	if len(history) >= 13 { // 1小时 = 12个5分钟
		current := history[len(history)-1]
		oi1hAgo := history[len(history)-13]
		if oi1hAgo > 0 {
			change1h = ((current - oi1hAgo) / oi1hAgo) * 100
		}
	}

	if len(history) >= 49 { // 4小时 = 48个5分钟
		current := history[len(history)-1]
		oi4hAgo := history[len(history)-49]
		if oi4hAgo > 0 {
			change4h = ((current - oi4hAgo) / oi4hAgo) * 100
		}
	}

	return history, change1h, change4h
}

// getFundingRate 获取资金费率
func getFundingRate(symbol string) (float64, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/premiumIndex?symbol=%s", symbol)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Symbol          string `json:"symbol"`
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
		InterestRate    string `json:"interestRate"`
		Time            int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	rate, _ := strconv.ParseFloat(result.LastFundingRate, 64)
	return rate, nil
}

// calculateIntradaySeriesEnhanced 计算增强版日内系列数据
func calculateIntradaySeriesEnhanced(klines []Kline) *IntradayData {
	data := &IntradayData{
		MidPrices:   make([]float64, 0, 10),
		HighPrices:  make([]float64, 0, 10),
		LowPrices:   make([]float64, 0, 10),
		Volumes:     make([]float64, 0, 10),
		EMA20Values: make([]float64, 0, 10),
		EMA50Values: make([]float64, 0, 10),
		MACDValues:  make([]float64, 0, 10),
		MACDSignal:  make([]float64, 0, 10),
		MACDHist:    make([]float64, 0, 10),
		RSI7Values:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
		ATRValues:   make([]float64, 0, 10),
	}

	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)
		data.HighPrices = append(data.HighPrices, klines[i].High)
		data.LowPrices = append(data.LowPrices, klines[i].Low)
		data.Volumes = append(data.Volumes, klines[i].Volume)

		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Values = append(data.EMA20Values, ema20)
		}

		if i >= 49 {
			ema50 := calculateEMA(klines[:i+1], 50)
			data.EMA50Values = append(data.EMA50Values, ema50)
		}

		if i >= 34 {
			macdLine, signalLine, hist := calculateMACDFull(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macdLine)
			data.MACDSignal = append(data.MACDSignal, signalLine)
			data.MACDHist = append(data.MACDHist, hist)
		}

		if i >= 7 {
			rsi7 := calculateRSI(klines[:i+1], 7)
			data.RSI7Values = append(data.RSI7Values, rsi7)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
			atr := calculateATR(klines[:i+1], 14)
			data.ATRValues = append(data.ATRValues, atr)
		}
	}

	return data
}

// calculateMidTermSeries15mEnhanced 计算增强版15分钟系列数据
func calculateMidTermSeries15mEnhanced(klines []Kline) *MidTermData15m {
	data := &MidTermData15m{
		MidPrices:   make([]float64, 0, 10),
		HighPrices:  make([]float64, 0, 10),
		LowPrices:   make([]float64, 0, 10),
		Volumes:     make([]float64, 0, 10),
		EMA20Values: make([]float64, 0, 10),
		EMA50Values: make([]float64, 0, 10),
		MACDValues:  make([]float64, 0, 10),
		MACDSignal:  make([]float64, 0, 10),
		MACDHist:    make([]float64, 0, 10),
		RSI7Values:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
		ADXValues:   make([]float64, 0, 10),
		ATRValues:   make([]float64, 0, 10),
	}

	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)
		data.HighPrices = append(data.HighPrices, klines[i].High)
		data.LowPrices = append(data.LowPrices, klines[i].Low)
		data.Volumes = append(data.Volumes, klines[i].Volume)

		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Values = append(data.EMA20Values, ema20)
		}

		if i >= 49 {
			ema50 := calculateEMA(klines[:i+1], 50)
			data.EMA50Values = append(data.EMA50Values, ema50)
		}

		if i >= 34 {
			macdLine, signalLine, hist := calculateMACDFull(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macdLine)
			data.MACDSignal = append(data.MACDSignal, signalLine)
			data.MACDHist = append(data.MACDHist, hist)
		}

		if i >= 7 {
			rsi7 := calculateRSI(klines[:i+1], 7)
			data.RSI7Values = append(data.RSI7Values, rsi7)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
			atr := calculateATR(klines[:i+1], 14)
			data.ATRValues = append(data.ATRValues, atr)
		}

		if i >= 28 {
			adx, _, _ := calculateADX(klines[:i+1], 14)
			data.ADXValues = append(data.ADXValues, adx)
		}
	}

	return data
}

// calculateMidTermSeries1hEnhanced 计算增强版1小时系列数据
func calculateMidTermSeries1hEnhanced(klines []Kline) *MidTermData1h {
	data := &MidTermData1h{
		MidPrices:   make([]float64, 0, 10),
		HighPrices:  make([]float64, 0, 10),
		LowPrices:   make([]float64, 0, 10),
		Volumes:     make([]float64, 0, 10),
		EMA20Values: make([]float64, 0, 10),
		EMA50Values: make([]float64, 0, 10),
		MACDValues:  make([]float64, 0, 10),
		MACDSignal:  make([]float64, 0, 10),
		MACDHist:    make([]float64, 0, 10),
		RSI7Values:  make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
		ADXValues:   make([]float64, 0, 10),
		ATRValues:   make([]float64, 0, 10),
	}

	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)
		data.HighPrices = append(data.HighPrices, klines[i].High)
		data.LowPrices = append(data.LowPrices, klines[i].Low)
		data.Volumes = append(data.Volumes, klines[i].Volume)

		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Values = append(data.EMA20Values, ema20)
		}

		if i >= 49 {
			ema50 := calculateEMA(klines[:i+1], 50)
			data.EMA50Values = append(data.EMA50Values, ema50)
		}

		if i >= 34 {
			macdLine, signalLine, hist := calculateMACDFull(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macdLine)
			data.MACDSignal = append(data.MACDSignal, signalLine)
			data.MACDHist = append(data.MACDHist, hist)
		}

		if i >= 7 {
			rsi7 := calculateRSI(klines[:i+1], 7)
			data.RSI7Values = append(data.RSI7Values, rsi7)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
			atr := calculateATR(klines[:i+1], 14)
			data.ATRValues = append(data.ATRValues, atr)
		}

		if i >= 28 {
			adx, _, _ := calculateADX(klines[:i+1], 14)
			data.ADXValues = append(data.ADXValues, adx)
		}
	}

	return data
}

// calculateLongerTermDataEnhanced 计算增强版长期数据
func calculateLongerTermDataEnhanced(klines []Kline) *LongerTermData {
	data := &LongerTermData{
		MACDValues:  make([]float64, 0, 10),
		MACDSignal:  make([]float64, 0, 10),
		MACDHist:    make([]float64, 0, 10),
		RSI14Values: make([]float64, 0, 10),
		ADXValues:   make([]float64, 0, 10),
		DIPlus:      make([]float64, 0, 10),
		DIMinus:     make([]float64, 0, 10),
	}

	// 计算EMA
	data.EMA20 = calculateEMA(klines, 20)
	data.EMA50 = calculateEMA(klines, 50)

	// 计算ATR
	data.ATR3 = calculateATR(klines, 3)
	data.ATR14 = calculateATR(klines, 14)
	if data.ATR14 > 0 {
		data.ATRRatio = data.ATR3 / data.ATR14
	}

	// 计算成交量
	if len(klines) > 0 {
		data.CurrentVolume = klines[len(klines)-1].Volume
		sum := 0.0
		for _, k := range klines {
			sum += k.Volume
		}
		data.AverageVolume = sum / float64(len(klines))
		if data.AverageVolume > 0 {
			data.VolumeRatio = data.CurrentVolume / data.AverageVolume
		}
	}

	// 计算布林带
	data.BollingerUpper, data.BollingerLower, data.BollingerWidth = calculateBollingerBands(klines, 20, 2.0)
	if data.BollingerUpper > data.BollingerLower {
		currentPrice := klines[len(klines)-1].Close
		data.PricePosition = (currentPrice - data.BollingerLower) / (data.BollingerUpper - data.BollingerLower)
	}

	// 计算序列指标
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		if i >= 34 {
			macdLine, signalLine, hist := calculateMACDFull(klines[:i+1])
			data.MACDValues = append(data.MACDValues, macdLine)
			data.MACDSignal = append(data.MACDSignal, signalLine)
			data.MACDHist = append(data.MACDHist, hist)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
		if i >= 28 {
			adx, diPlus, diMinus := calculateADX(klines[:i+1], 14)
			data.ADXValues = append(data.ADXValues, adx)
			data.DIPlus = append(data.DIPlus, diPlus)
			data.DIMinus = append(data.DIMinus, diMinus)
		}
	}

	return data
}

// Format 格式化输出市场数据（优化版）
// Format 格式化输出市场数据（优化版）- 续
func Format(data *Data) string {
	var sb strings.Builder

	// 基础信息
	sb.WriteString(fmt.Sprintf("**价格**: %.4f | **1h**: %+.2f%% | **4h**: %+.2f%%\n",
		data.CurrentPrice, data.PriceChange1h, data.PriceChange4h))

	sb.WriteString(fmt.Sprintf("**EMA20**: %.4f | **EMA50**: %.4f | **MACD**: %.6f | **RSI7**: %.1f | **RSI14**: %.1f\n",
		data.CurrentEMA20, data.CurrentEMA50, data.CurrentMACD, data.CurrentRSI7, data.CurrentRSI14))

	// ADX趋势强度
	trendStrength := "震荡"
	if data.CurrentADX > 25 {
		if data.CurrentDIPlus > data.CurrentDIMinus {
			trendStrength = "强上升趋势"
		} else {
			trendStrength = "强下降趋势"
		}
	} else if data.CurrentADX > 20 {
		trendStrength = "趋势形成中"
	}
	sb.WriteString(fmt.Sprintf("**ADX**: %.1f (**%s**) | **DI+**: %.1f | **DI-**: %.1f | **布林宽度**: %.2f%%\n\n",
		data.CurrentADX, trendStrength, data.CurrentDIPlus, data.CurrentDIMinus, data.BollingerWidth))

	// OI和资金费率（优化版）
	if data.OpenInterest != nil {
		oiValueMil := data.OIValueUSD / 1_000_000
		sb.WriteString(fmt.Sprintf("**OI**: %.2f (价值: **%.2fM USD**) | **OI变化1h**: %+.2f%% | **OI变化4h**: %+.2f%%\n",
			data.OpenInterest.Latest, oiValueMil, data.OpenInterest.Change1h, data.OpenInterest.Change4h))
	}

	// 资金费率解读
	fundingRatePct := data.FundingRate * 100
	fundingSignal := "中性"
	if fundingRatePct > 0.05 {
		fundingSignal = "极度看多(警惕反转)"
	} else if fundingRatePct > 0.03 {
		fundingSignal = "偏多"
	} else if fundingRatePct < -0.05 {
		fundingSignal = "极度看空(警惕反转)"
	} else if fundingRatePct < -0.03 {
		fundingSignal = "偏空"
	}
	sb.WriteString(fmt.Sprintf("**资金费率**: %.4f%% (**%s**) | 年化: %.1f%%\n\n",
		fundingRatePct, fundingSignal, fundingRatePct*3*365))

	// 3分钟数据
	if data.IntradaySeries != nil {
		sb.WriteString("**📊 3分钟数据** (实时入场)\n")
		if len(data.IntradaySeries.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("  价格: %s\n", formatFloatSlice(data.IntradaySeries.MidPrices)))
		}
		if len(data.IntradaySeries.Volumes) > 0 {
			sb.WriteString(fmt.Sprintf("  成交量: %s\n", formatFloatSlice(data.IntradaySeries.Volumes)))
		}
		if len(data.IntradaySeries.MACDHist) > 0 {
			sb.WriteString(fmt.Sprintf("  MACD柱: %s\n", formatFloatSlice(data.IntradaySeries.MACDHist)))
		}
		if len(data.IntradaySeries.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("  RSI14: %s\n", formatFloatSlice(data.IntradaySeries.RSI14Values)))
		}
		sb.WriteString("\n")
	}

	// 15分钟数据
	if data.MidTermSeries15m != nil {
		sb.WriteString("**📊 15分钟数据** (短期趋势)\n")
		if len(data.MidTermSeries15m.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("  价格: %s\n", formatFloatSlice(data.MidTermSeries15m.MidPrices)))
		}
		if len(data.MidTermSeries15m.EMA20Values) > 0 && len(data.MidTermSeries15m.EMA50Values) > 0 {
			sb.WriteString(fmt.Sprintf("  EMA20: %s\n", formatFloatSlice(data.MidTermSeries15m.EMA20Values)))
			sb.WriteString(fmt.Sprintf("  EMA50: %s\n", formatFloatSlice(data.MidTermSeries15m.EMA50Values)))
		}
		if len(data.MidTermSeries15m.MACDHist) > 0 {
			sb.WriteString(fmt.Sprintf("  MACD柱: %s\n", formatFloatSlice(data.MidTermSeries15m.MACDHist)))
		}
		if len(data.MidTermSeries15m.ADXValues) > 0 {
			sb.WriteString(fmt.Sprintf("  ADX: %s\n", formatFloatSlice(data.MidTermSeries15m.ADXValues)))
		}
		sb.WriteString("\n")
	}

	// 1小时数据
	if data.MidTermSeries1h != nil {
		sb.WriteString("**📊 1小时数据** (中期趋势)\n")
		if len(data.MidTermSeries1h.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("  价格: %s\n", formatFloatSlice(data.MidTermSeries1h.MidPrices)))
		}
		if len(data.MidTermSeries1h.EMA20Values) > 0 && len(data.MidTermSeries1h.EMA50Values) > 0 {
			sb.WriteString(fmt.Sprintf("  EMA20: %s\n", formatFloatSlice(data.MidTermSeries1h.EMA20Values)))
			sb.WriteString(fmt.Sprintf("  EMA50: %s\n", formatFloatSlice(data.MidTermSeries1h.EMA50Values)))
		}
		if len(data.MidTermSeries1h.MACDHist) > 0 {
			sb.WriteString(fmt.Sprintf("  MACD柱: %s\n", formatFloatSlice(data.MidTermSeries1h.MACDHist)))
		}
		if len(data.MidTermSeries1h.ADXValues) > 0 {
			sb.WriteString(fmt.Sprintf("  ADX: %s\n", formatFloatSlice(data.MidTermSeries1h.ADXValues)))
		}
		if len(data.MidTermSeries1h.ATRValues) > 0 {
			sb.WriteString(fmt.Sprintf("  ATR: %s\n", formatFloatSlice(data.MidTermSeries1h.ATRValues)))
		}
		sb.WriteString("\n")
	}

	// 4小时数据（优化版）
	if data.LongerTermContext != nil {
		sb.WriteString("**📊 4小时数据** (主趋势)\n")

		// EMA关系判断
		emaRelation := "横盘"
		if data.LongerTermContext.EMA20 > data.LongerTermContext.EMA50*1.005 {
			emaRelation = "多头排列(EMA20>EMA50)"
		} else if data.LongerTermContext.EMA20 < data.LongerTermContext.EMA50*0.995 {
			emaRelation = "空头排列(EMA20<EMA50)"
		}
		sb.WriteString(fmt.Sprintf("  EMA20: %.4f | EMA50: %.4f | **%s**\n",
			data.LongerTermContext.EMA20, data.LongerTermContext.EMA50, emaRelation))

		sb.WriteString(fmt.Sprintf("  ATR3: %.4f | ATR14: %.4f | ATR比率: %.2f\n",
			data.LongerTermContext.ATR3, data.LongerTermContext.ATR14, data.LongerTermContext.ATRRatio))

		// 成交量分析
		volumeSignal := "正常"
		if data.LongerTermContext.VolumeRatio > 1.5 {
			volumeSignal = "放量"
		} else if data.LongerTermContext.VolumeRatio < 0.5 {
			volumeSignal = "缩量"
		}
		sb.WriteString(fmt.Sprintf("  成交量: %.2f / 均值: %.2f | 比率: %.2f (**%s**)\n",
			data.LongerTermContext.CurrentVolume, data.LongerTermContext.AverageVolume,
			data.LongerTermContext.VolumeRatio, volumeSignal))

		// 布林带位置
		positionDesc := "中轨"
		if data.LongerTermContext.PricePosition > 0.8 {
			positionDesc = "接近上轨(超买)"
		} else if data.LongerTermContext.PricePosition < 0.2 {
			positionDesc = "接近下轨(超卖)"
		} else if data.LongerTermContext.PricePosition > 0.5 {
			positionDesc = "中上区域"
		} else {
			positionDesc = "中下区域"
		}
		sb.WriteString(fmt.Sprintf("  布林带: 上轨%.4f | 下轨%.4f | 宽度%.2f%% | 位置: **%s**(%.1f%%)\n",
			data.LongerTermContext.BollingerUpper, data.LongerTermContext.BollingerLower,
			data.LongerTermContext.BollingerWidth, positionDesc, data.LongerTermContext.PricePosition*100))

		if len(data.LongerTermContext.MACDHist) > 0 {
			sb.WriteString(fmt.Sprintf("  MACD柱: %s\n", formatFloatSlice(data.LongerTermContext.MACDHist)))
		}
		if len(data.LongerTermContext.ADXValues) > 0 {
			sb.WriteString(fmt.Sprintf("  ADX: %s\n", formatFloatSlice(data.LongerTermContext.ADXValues)))
		}
		if len(data.LongerTermContext.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("  RSI14: %s\n", formatFloatSlice(data.LongerTermContext.RSI14Values)))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// formatFloatSlice 格式化float64切片为字符串
func formatFloatSlice(values []float64) string {
	strValues := make([]string, len(values))
	for i, v := range values {
		if math.Abs(v) < 0.01 {
			strValues[i] = fmt.Sprintf("%.6f", v)
		} else if math.Abs(v) < 1 {
			strValues[i] = fmt.Sprintf("%.4f", v)
		} else if math.Abs(v) < 100 {
			strValues[i] = fmt.Sprintf("%.2f", v)
		} else {
			strValues[i] = fmt.Sprintf("%.0f", v)
		}
	}
	return "[" + strings.Join(strValues, ", ") + "]"
}

// Normalize 标准化symbol,确保是USDT交易对
func Normalize(symbol string) string {
	symbol = strings.ToUpper(symbol)
	if strings.HasSuffix(symbol, "USDT") {
		return symbol
	}
	return symbol + "USDT"
}

// parseFloat 解析float值
func parseFloat(v interface{}) (float64, error) {
	switch val := v.(type) {
	case string:
		return strconv.ParseFloat(val, 64)
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	default:
		return 0, fmt.Errorf("unsupported type: %T", v)
	}
}

// CalculateAdaptivePositionSize 计算波动率自适应仓位（新增核心函数）
func CalculateAdaptivePositionSize(equity, atr, currentPrice, riskPct float64, isAltcoin bool) (positionSize, stopDistance float64) {
	// ATR倍数：山寨币波动大，用更大倍数
	multiplier := 2.5 // BTC/ETH
	if isAltcoin {
		multiplier = 3.5
	}

	// 止损距离 = ATR × 倍数
	stopDistance = atr * multiplier

	// 止损百分比
	stopPct := stopDistance / currentPrice

	// 仓位大小 = (账户净值 × 风险比例) / 止损百分比
	if stopPct > 0 {
		positionSize = (equity * riskPct) / stopPct
	}

	return positionSize, stopDistance
}

// CalculateCorrelation 计算两个价格序列的相关系数（新增）
func CalculateCorrelation(prices1, prices2 []float64) float64 {
	n := len(prices1)
	if n != len(prices2) || n < 2 {
		return 0
	}

	// 计算均值
	mean1, mean2 := 0.0, 0.0
	for i := 0; i < n; i++ {
		mean1 += prices1[i]
		mean2 += prices2[i]
	}
	mean1 /= float64(n)
	mean2 /= float64(n)

	// 计算协方差和标准差
	var cov, var1, var2 float64
	for i := 0; i < n; i++ {
		d1 := prices1[i] - mean1
		d2 := prices2[i] - mean2
		cov += d1 * d2
		var1 += d1 * d1
		var2 += d2 * d2
	}

	if var1 == 0 || var2 == 0 {
		return 0
	}

	return cov / math.Sqrt(var1*var2)
}

// GetMarketState 获取市场状态（新增：量化识别）
func GetMarketState(data *Data) (state string, confidence int) {
	adx := data.CurrentADX
	diPlus := data.CurrentDIPlus
	diMinus := data.CurrentDIMinus
	bollingerWidth := data.BollingerWidth

	// 基于ADX判断趋势强度
	if adx > 25 {
		if diPlus > diMinus {
			state = "STRONG_UPTREND"
			confidence = int(math.Min(float64(adx)*2, 95))
		} else {
			state = "STRONG_DOWNTREND"
			confidence = int(math.Min(float64(adx)*2, 95))
		}
	} else if adx > 20 {
		if diPlus > diMinus {
			state = "WEAK_UPTREND"
		} else {
			state = "WEAK_DOWNTREND"
		}
		confidence = int(adx * 2)
	} else {
		state = "RANGING"
		confidence = 100 - int(adx*2) // 震荡时ADX越低，震荡确认度越高
	}

	// 布林带宽度修正
	if bollingerWidth < 2.0 && state == "RANGING" {
		state = "SQUEEZE" // 波动收缩，可能即将突破
		confidence = 80
	}

	return state, confidence
}

// DetectDivergence 检测MACD背离（新增）
func DetectDivergence(prices, macdValues []float64) (bullishDiv, bearishDiv bool) {
	if len(prices) < 3 || len(macdValues) < 3 {
		return false, false
	}

	n := len(prices)

	// 检查最近3个点
	// 看涨背离：价格新低，MACD未新低
	if prices[n-1] < prices[n-2] && prices[n-1] < prices[n-3] {
		if macdValues[n-1] > macdValues[n-2] || macdValues[n-1] > macdValues[n-3] {
			bullishDiv = true
		}
	}

	// 看跌背离：价格新高，MACD未新高
	if prices[n-1] > prices[n-2] && prices[n-1] > prices[n-3] {
		if macdValues[n-1] < macdValues[n-2] || macdValues[n-1] < macdValues[n-3] {
			bearishDiv = true
		}
	}

	return bullishDiv, bearishDiv
}
