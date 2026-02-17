package binance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
)

type wsOrderResult struct {
	OrderID       int64  `json:"orderId"`
	ClientOrderID string `json:"clientOrderId"`
	Status        string `json:"status"`
}

type orderWSConn struct {
	conn *websocket.Conn
	stop chan struct{}
}

func (c *Client) PlaceOrder(ctx context.Context, order core.Order) (core.Order, error) {
	if order.ClientID == "" {
		order.ClientID = newClientOrderID(c.getClientOrderPrefix())
	}
	placed, err := c.placeOrderWS(ctx, order)
	if err == nil {
		if c.clearWSDegraded() {
			c.alertImportant("ws_order_recovered", map[string]string{
				"symbol": order.Symbol,
			})
		}
		return placed, nil
	}
	c.markWSDegraded()
	c.alertImportant("ws_order_fallback_to_rest", map[string]string{
		"symbol":    order.Symbol,
		"side":      string(order.Side),
		"type":      string(order.Type),
		"price":     order.Price.String(),
		"qty":       order.Qty.String(),
		"client_id": order.ClientID,
		"ws_error":  err.Error(),
	})
	placed, restErr := c.placeOrderREST(ctx, order)
	if restErr != nil {
		c.alertImportant("rest_order_failed", map[string]string{
			"symbol":    order.Symbol,
			"side":      string(order.Side),
			"type":      string(order.Type),
			"price":     order.Price.String(),
			"qty":       order.Qty.String(),
			"client_id": order.ClientID,
			"rest_err":  restErr.Error(),
		})
	}
	return placed, restErr
}

func (c *Client) placeOrderWS(ctx context.Context, order core.Order) (core.Order, error) {
	if c.wsBaseURL == "" {
		return core.Order{}, errors.New("ws base url required")
	}
	c.orderMu.Lock()
	defer c.orderMu.Unlock()

	conn, err := c.ensureOrderConn(ctx)
	if err != nil {
		return core.Order{}, err
	}

	params, err := c.wsOrderParams(order)
	if err != nil {
		return core.Order{}, err
	}
	resp, err := sendWSRequest(ctx, conn, "order.place", params)
	if err != nil {
		c.resetOrderConn()
		return core.Order{}, err
	}
	var result wsOrderResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return core.Order{}, err
	}
	order.ID = strconv.FormatInt(result.OrderID, 10)
	if result.Status != "" {
		order.Status = core.OrderStatus(result.Status)
	} else {
		order.Status = core.OrderNew
	}
	if order.ClientID == "" && result.ClientOrderID != "" {
		order.ClientID = result.ClientOrderID
	}
	return order, nil
}

func (c *Client) wsOrderParams(order core.Order) (map[string]interface{}, error) {
	if order.Symbol == "" {
		return nil, errors.New("symbol required")
	}
	if order.Qty.Cmp(decimal.Zero) <= 0 {
		return nil, errors.New("invalid order")
	}
	if order.Type == core.Limit && order.Price.Cmp(decimal.Zero) <= 0 {
		return nil, errors.New("invalid order price")
	}
	ts := time.Now().UnixMilli()
	params := map[string]interface{}{
		"symbol":    order.Symbol,
		"side":      string(order.Side),
		"type":      string(order.Type),
		"quantity":  order.Qty.String(),
		"timestamp": ts,
	}
	if order.Type == core.Limit {
		params["timeInForce"] = "GTC"
		params["price"] = order.Price.String()
	}
	if order.ClientID != "" {
		params["newClientOrderId"] = order.ClientID
	}
	if c.recvWindow > 0 {
		params["recvWindow"] = c.recvWindow.Milliseconds()
	}

	if c.userStreamAuth != "session" {
		if c.apiKey == "" || c.apiSecret == "" {
			return nil, errors.New("api_key/api_secret required")
		}
		values := url.Values{}
		values.Set("apiKey", c.apiKey)
		values.Set("symbol", order.Symbol)
		values.Set("side", string(order.Side))
		values.Set("type", string(order.Type))
		values.Set("quantity", order.Qty.String())
		if order.Type == core.Limit {
			values.Set("timeInForce", "GTC")
			values.Set("price", order.Price.String())
		}
		values.Set("timestamp", strconv.FormatInt(ts, 10))
		if order.ClientID != "" {
			values.Set("newClientOrderId", order.ClientID)
		}
		if c.recvWindow > 0 {
			values.Set("recvWindow", strconv.FormatInt(c.recvWindow.Milliseconds(), 10))
		}
		signature := sign(c.apiSecret, values.Encode())
		params["apiKey"] = c.apiKey
		params["signature"] = signature
	}

	return params, nil
}

func (c *Client) placeOrderREST(ctx context.Context, order core.Order) (core.Order, error) {
	params := url.Values{}
	params.Set("symbol", order.Symbol)
	params.Set("side", string(order.Side))
	params.Set("type", string(order.Type))
	params.Set("quantity", order.Qty.String())
	if order.Type == core.Limit {
		params.Set("timeInForce", "GTC")
		params.Set("price", order.Price.String())
	}
	if order.ClientID != "" {
		params.Set("newClientOrderId", order.ClientID)
	}

	body, err := c.doRequest(ctx, http.MethodPost, "/api/v3/order", params, AuthSigned)
	if err != nil {
		if apiErr, ok := err.(APIError); ok {
			if isRejectOrExpireStatus(apiErr.Msg) {
				c.alertImportant("order_rejected_or_expired", map[string]string{
					"symbol":     order.Symbol,
					"side":       string(order.Side),
					"type":       string(order.Type),
					"client_id":  order.ClientID,
					"error_code": strconv.Itoa(apiErr.Code),
					"error_msg":  apiErr.Msg,
				})
			}
		}
		if apiErr, ok := err.(APIError); ok && apiErr.Code == -2010 && strings.Contains(strings.ToLower(apiErr.Msg), "duplicate") {
			if order.ClientID != "" {
				if existing, err := c.getOrderByClientID(ctx, order.Symbol, order.ClientID); err == nil {
					return existing, nil
				}
			}
		}
		return core.Order{}, err
	}
	var resp orderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return core.Order{}, err
	}
	order.ID = strconv.FormatInt(resp.OrderID, 10)
	order.Status = core.OrderNew
	return order, nil
}

func isRejectOrExpireStatus(v string) bool {
	s := strings.ToUpper(v)
	return strings.Contains(s, "REJECT") || strings.Contains(s, "EXPIRE")
}

func (c *Client) ensureOrderConn(ctx context.Context) (*websocket.Conn, error) {
	if c.orderConn != nil {
		return c.orderConn.conn, nil
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.wsBaseURL, nil)
	if err != nil {
		return nil, err
	}
	if c.userStreamAuth == "session" {
		if err := c.sessionLogon(ctx, conn); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	ow := &orderWSConn{conn: conn, stop: make(chan struct{})}
	c.orderConn = ow
	if c.orderWSKeepalive > 0 {
		go c.orderKeepaliveLoop(ow)
	}
	return conn, nil
}

func (c *Client) resetOrderConn() {
	if c.orderConn == nil {
		return
	}
	close(c.orderConn.stop)
	_ = c.orderConn.conn.Close()
	c.orderConn = nil
}

func (c *Client) getOrderByClientID(ctx context.Context, symbol, clientID string) (core.Order, error) {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("origClientOrderId", clientID)
	body, err := c.doRequest(ctx, http.MethodGet, "/api/v3/order", params, AuthSigned)
	if err != nil {
		return core.Order{}, err
	}
	var resp orderQueryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return core.Order{}, err
	}
	price, err := decimal.NewFromString(resp.Price)
	if err != nil {
		return core.Order{}, err
	}
	qty, err := decimal.NewFromString(resp.OrigQty)
	if err != nil {
		return core.Order{}, err
	}
	return core.Order{
		ID:       strconv.FormatInt(resp.OrderID, 10),
		ClientID: resp.ClientOrderID,
		Symbol:   resp.Symbol,
		Side:     core.Side(resp.Side),
		Type:     core.OrderType(resp.Type),
		Price:    price,
		Qty:      qty,
		Status:   core.OrderStatus(resp.Status),
	}, nil
}

var orderSeq uint64

func newClientOrderID(prefix string) string {
	if prefix == "" {
		prefix = "gt"
	}
	tsPart := strconv.FormatInt(time.Now().UnixNano(), 36)
	seqPart := strconv.FormatUint(atomic.AddUint64(&orderSeq, 1), 36)
	suffix := tsPart + "-" + seqPart
	maxPrefix := 36 - 1 - len(suffix)
	if maxPrefix < 1 {
		maxPrefix = 1
	}
	if len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}
	return prefix + "-" + suffix
}

func (c *Client) orderKeepaliveLoop(ow *orderWSConn) {
	ticker := time.NewTicker(c.orderWSKeepalive)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.orderMu.Lock()
			if c.orderConn == nil || c.orderConn != ow {
				c.orderMu.Unlock()
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := sendWSRequest(ctx, ow.conn, "ping", nil)
			cancel()
			if err != nil {
				c.resetOrderConn()
				c.orderMu.Unlock()
				return
			}
			c.orderMu.Unlock()
		case <-ow.stop:
			return
		}
	}
}
