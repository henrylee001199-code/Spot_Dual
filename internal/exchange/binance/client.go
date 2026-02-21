package binance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"grid-trading/internal/alert"
	"grid-trading/internal/config"
	"grid-trading/internal/core"
)

type AuthType int

const (
	AuthNone AuthType = iota
	AuthAPIKey
	AuthSigned
)

type Client struct {
	apiKey            string
	apiSecret         string
	baseURL           string
	wsBaseURL         string
	symbol            string
	clientOrderPrefix string
	userStreamAuth    string
	orderMu           sync.Mutex
	orderConn         *orderWSConn
	orderWSKeepalive  time.Duration
	alerter           alert.Alerter

	recvWindow time.Duration
	httpClient *http.Client

	mu          sync.Mutex
	symbolCache map[string]symbolInfo
	wsDegraded  bool
}

type Options struct {
	APIKey              string
	APISecret           string
	RestBaseURL         string
	WSBaseURL           string
	Symbol              string
	ClientOrderPrefix   string
	UserStreamAuth      string
	RecvWindowMs        int64
	HTTPTimeoutSec      int64
	OrderWSKeepaliveSec int64
}

func NewClient(cfg config.ExchangeConfig, symbol, instanceID string) (*Client, error) {
	if cfg.APIKey == "" || cfg.APISecret == "" {
		return nil, errors.New("api_key/api_secret required")
	}
	opts := Options{
		APIKey:              cfg.APIKey,
		APISecret:           cfg.APISecret,
		RestBaseURL:         cfg.RestBaseURL,
		WSBaseURL:           cfg.WSBaseURL,
		Symbol:              symbol,
		ClientOrderPrefix:   instanceID,
		RecvWindowMs:        cfg.RecvWindowMs,
		HTTPTimeoutSec:      cfg.HTTPTimeoutSec,
		OrderWSKeepaliveSec: cfg.OrderWSKeepaliveSec,
	}
	return NewClientWithOptions(opts), nil
}

func NewClientWithOptions(opts Options) *Client {
	timeout := 15 * time.Second
	if opts.HTTPTimeoutSec > 0 {
		timeout = time.Duration(opts.HTTPTimeoutSec) * time.Second
	}
	recvWindow := time.Duration(opts.RecvWindowMs) * time.Millisecond
	userStreamAuth := strings.ToLower(strings.TrimSpace(opts.UserStreamAuth))
	if userStreamAuth == "" {
		userStreamAuth = "signature"
	}
	orderKeepalive := time.Duration(opts.OrderWSKeepaliveSec) * time.Second
	return &Client{
		apiKey:            opts.APIKey,
		apiSecret:         opts.APISecret,
		baseURL:           strings.TrimRight(opts.RestBaseURL, "/"),
		wsBaseURL:         strings.TrimRight(opts.WSBaseURL, "/"),
		symbol:            opts.Symbol,
		clientOrderPrefix: normalizeClientOrderPrefix(opts.ClientOrderPrefix),
		userStreamAuth:    userStreamAuth,
		recvWindow:        recvWindow,
		httpClient:        &http.Client{Timeout: timeout},
		symbolCache:       make(map[string]symbolInfo),
		orderWSKeepalive:  orderKeepalive,
	}
}

func (c *Client) SetAlerter(alerter alert.Alerter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.alerter = alerter
}

func (c *Client) alertImportant(event string, fields map[string]string) {
	c.mu.Lock()
	alerter := c.alerter
	c.mu.Unlock()
	if alerter == nil {
		return
	}
	alerter.Important(event, fields)
}

func (c *Client) markWSDegraded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wsDegraded {
		return false
	}
	c.wsDegraded = true
	return true
}

func (c *Client) clearWSDegraded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.wsDegraded {
		return false
	}
	c.wsDegraded = false
	return true
}

func (c *Client) getClientOrderPrefix() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clientOrderPrefix == "" {
		return "gt"
	}
	return c.clientOrderPrefix
}

func normalizeClientOrderPrefix(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "gt"
	}
	b := strings.Builder{}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "gt"
	}
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}

func (c *Client) Name() string { return "binance" }

func (c *Client) Close() error {
	c.orderMu.Lock()
	defer c.orderMu.Unlock()
	c.resetOrderConn()
	return nil
}

func (c *Client) GetRules(ctx context.Context, symbol string) (core.Rules, error) {
	info, err := c.getSymbolInfo(ctx, symbol)
	if err != nil {
		return core.Rules{}, err
	}
	return info.rules, nil
}

func (c *Client) ConfigureFutures(ctx context.Context, symbol, positionMode, marginType string, leverage int) error {
	if err := c.SetPositionMode(ctx, positionMode); err != nil {
		return fmt.Errorf("set position mode: %w", err)
	}
	if symbol == "" {
		return nil
	}
	if err := c.SetMarginType(ctx, symbol, marginType); err != nil {
		return fmt.Errorf("set margin type: %w", err)
	}
	if err := c.SetLeverage(ctx, symbol, leverage); err != nil {
		return fmt.Errorf("set leverage: %w", err)
	}
	return nil
}

func (c *Client) SetPositionMode(ctx context.Context, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return nil
	}
	dual := false
	switch mode {
	case "hedge":
		dual = true
	case "oneway", "one_way":
		dual = false
	default:
		return fmt.Errorf("unsupported position mode: %s", mode)
	}
	params := url.Values{}
	params.Set("dualSidePosition", strconv.FormatBool(dual))
	_, err := c.doRequest(ctx, http.MethodPost, "/fapi/v1/positionSide/dual", params, AuthSigned)
	if err != nil && IsAPIErrorCode(err, -4059) {
		return nil
	}
	return err
}

func (c *Client) SetMarginType(ctx context.Context, symbol, marginType string) error {
	if symbol == "" {
		return errors.New("symbol required")
	}
	mt := strings.ToLower(strings.TrimSpace(marginType))
	if mt == "" {
		return nil
	}
	apiMarginType := ""
	switch mt {
	case "cross":
		apiMarginType = "CROSSED"
	case "isolated":
		apiMarginType = "ISOLATED"
	default:
		return fmt.Errorf("unsupported margin type: %s", marginType)
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("marginType", apiMarginType)
	_, err := c.doRequest(ctx, http.MethodPost, "/fapi/v1/marginType", params, AuthSigned)
	if err != nil && IsAPIErrorCode(err, -4046) {
		return nil
	}
	return err
}

func (c *Client) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	if symbol == "" {
		return errors.New("symbol required")
	}
	if leverage < 1 {
		return errors.New("leverage must be >= 1")
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("leverage", strconv.Itoa(leverage))
	_, err := c.doRequest(ctx, http.MethodPost, "/fapi/v1/leverage", params, AuthSigned)
	return err
}

func (c *Client) CancelOrder(ctx context.Context, symbol, orderID string) error {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", orderID)
	_, err := c.doRequest(ctx, http.MethodDelete, "/fapi/v1/order", params, AuthSigned)
	return err
}

func (c *Client) OpenOrders(ctx context.Context, symbol string) ([]core.Order, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	body, err := c.doRequest(ctx, http.MethodGet, "/fapi/v1/openOrders", params, AuthSigned)
	if err != nil {
		return nil, err
	}
	var resp []openOrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	orders := make([]core.Order, 0, len(resp))
	for _, ord := range resp {
		price, _ := decimal.NewFromString(ord.Price)
		origQty, _ := decimal.NewFromString(ord.OrigQty)
		executedQty, _ := decimal.NewFromString(ord.ExecutedQty)
		qty := origQty
		if executedQty.Cmp(decimal.Zero) > 0 && origQty.Cmp(executedQty) > 0 {
			qty = origQty.Sub(executedQty)
		}
		createdAt := time.Time{}
		if ord.UpdateTime > 0 {
			createdAt = time.UnixMilli(ord.UpdateTime)
		}
		orders = append(orders, core.Order{
			ID:           strconv.FormatInt(ord.OrderID, 10),
			ClientID:     ord.ClientOrderID,
			Symbol:       ord.Symbol,
			Side:         core.Side(ord.Side),
			Type:         core.OrderType(ord.Type),
			PositionSide: core.PositionSide(ord.PositionSide),
			ReduceOnly:   ord.ReduceOnly,
			Price:        price,
			Qty:          qty,
			Status:       core.OrderStatus(ord.Status),
			CreatedAt:    createdAt,
		})
	}
	return orders, nil
}

type OrderQuery struct {
	Order              core.Order
	ExecutedQty        decimal.Decimal
	CumulativeQuoteQty decimal.Decimal
	UpdateTime         time.Time
}

func (c *Client) QueryOrder(ctx context.Context, symbol, orderID, clientID string) (OrderQuery, error) {
	if symbol == "" {
		return OrderQuery{}, errors.New("symbol required")
	}
	if orderID == "" && clientID == "" {
		return OrderQuery{}, errors.New("orderID or clientID required")
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	if orderID != "" {
		params.Set("orderId", orderID)
	} else {
		params.Set("origClientOrderId", clientID)
	}
	body, err := c.doRequest(ctx, http.MethodGet, "/fapi/v1/order", params, AuthSigned)
	if err != nil {
		return OrderQuery{}, err
	}
	var resp orderQueryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return OrderQuery{}, err
	}
	price, _ := decimal.NewFromString(resp.Price)
	qty, _ := decimal.NewFromString(resp.OrigQty)
	executedQty, _ := decimal.NewFromString(resp.ExecutedQty)
	cumQuote, _ := decimal.NewFromString(resp.CumulativeQuote)
	if cumQuote.Cmp(decimal.Zero) <= 0 {
		cumQuote, _ = decimal.NewFromString(resp.CumulativeQuoteQty)
	}

	order := core.Order{
		ID:           strconv.FormatInt(resp.OrderID, 10),
		ClientID:     resp.ClientOrderID,
		Symbol:       resp.Symbol,
		Side:         core.Side(resp.Side),
		Type:         core.OrderType(resp.Type),
		PositionSide: core.PositionSide(resp.PositionSide),
		ReduceOnly:   resp.ReduceOnly,
		Price:        price,
		Qty:          qty,
		Status:       core.OrderStatus(resp.Status),
	}
	if resp.Time > 0 {
		order.CreatedAt = time.UnixMilli(resp.Time)
	}
	updateTime := time.Time{}
	if resp.UpdateTime > 0 {
		updateTime = time.UnixMilli(resp.UpdateTime)
	}
	return OrderQuery{
		Order:              order,
		ExecutedQty:        executedQty,
		CumulativeQuoteQty: cumQuote,
		UpdateTime:         updateTime,
	}, nil
}

func (c *Client) Balances(ctx context.Context) (core.Balance, error) {
	if c.symbol == "" {
		return core.Balance{}, errors.New("symbol is required to resolve balances")
	}
	info, err := c.getSymbolInfo(ctx, c.symbol)
	if err != nil {
		return core.Balance{}, err
	}
	body, err := c.doRequest(ctx, http.MethodGet, "/fapi/v2/balance", url.Values{}, AuthSigned)
	if err != nil {
		return core.Balance{}, err
	}
	var resp futuresBalanceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return core.Balance{}, err
	}
	bal := core.Balance{Base: decimal.Zero, Quote: decimal.Zero}
	for _, b := range resp {
		if b.Asset != info.quoteAsset {
			continue
		}
		total, _ := decimal.NewFromString(b.Balance)
		available, _ := decimal.NewFromString(b.AvailableBalance)
		if total.Cmp(decimal.Zero) > 0 {
			bal.Quote = total
		} else {
			bal.Quote = available
		}
		break
	}
	return bal, nil
}

func (c *Client) TickerPrice(ctx context.Context, symbol string) (decimal.Decimal, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	body, err := c.doRequest(ctx, http.MethodGet, "/fapi/v1/ticker/price", params, AuthNone)
	if err != nil {
		return decimal.Zero, err
	}
	var resp tickerPriceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return decimal.Zero, err
	}
	price, err := decimal.NewFromString(resp.Price)
	if err != nil {
		return decimal.Zero, err
	}
	return price, nil
}

func (c *Client) doRequest(ctx context.Context, method, path string, params url.Values, auth AuthType) ([]byte, error) {
	if auth == AuthSigned {
		params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
		if c.recvWindow > 0 {
			params.Set("recvWindow", strconv.FormatInt(c.recvWindow.Milliseconds(), 10))
		}
		signature := sign(c.apiSecret, params.Encode())
		params.Set("signature", signature)
	}
	var (
		req *http.Request
		err error
	)
	urlStr := c.baseURL + path
	if method == http.MethodGet || method == http.MethodDelete {
		if encoded := params.Encode(); encoded != "" {
			urlStr += "?" + encoded
		}
		req, err = http.NewRequestWithContext(ctx, method, urlStr, nil)
	} else {
		body := params.Encode()
		req, err = http.NewRequestWithContext(ctx, method, urlStr, strings.NewReader(body))
	}
	if err != nil {
		return nil, err
	}
	if method != http.MethodGet && method != http.MethodDelete {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if auth == AuthAPIKey || auth == AuthSigned {
		req.Header.Set("X-MBX-APIKEY", c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, parseAPIError(resp.StatusCode, body)
	}
	return body, nil
}

func parseAPIError(status int, body []byte) error {
	var apiErr apiError
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Msg != "" {
		return wrapAPIError(apiErr.Code, apiErr.Msg)
	}
	return fmt.Errorf("binance http error %d: %s", status, strings.TrimSpace(string(body)))
}

func sign(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *Client) getSymbolInfo(ctx context.Context, symbol string) (symbolInfo, error) {
	if symbol == "" {
		return symbolInfo{}, errors.New("symbol is required")
	}
	c.mu.Lock()
	if symbol != "" {
		if info, ok := c.symbolCache[symbol]; ok {
			c.mu.Unlock()
			return info, nil
		}
	}
	c.mu.Unlock()

	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}
	body, err := c.doRequest(ctx, http.MethodGet, "/fapi/v1/exchangeInfo", params, AuthNone)
	if err != nil {
		return symbolInfo{}, err
	}
	var resp exchangeInfoResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return symbolInfo{}, err
	}
	if len(resp.Symbols) == 0 {
		return symbolInfo{}, errors.New("symbol not found")
	}
	info := parseSymbolInfo(resp.Symbols[0])
	c.mu.Lock()
	if symbol != "" {
		c.symbolCache[symbol] = info
	}
	c.mu.Unlock()
	return info, nil
}
