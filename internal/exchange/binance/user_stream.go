package binance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
)

type UserStream struct {
	client    *Client
	conn      *websocket.Conn
	keepalive time.Duration
	listenKey string
}

type futuresOrderTradeUpdate struct {
	EventType       string `json:"e"`
	EventTime       int64  `json:"E"`
	TransactionTime int64  `json:"T"`
	Order           struct {
		Symbol        string `json:"s"`
		OrderID       int64  `json:"i"`
		Side          string `json:"S"`
		ExecutionType string `json:"x"`
		OrderStatus   string `json:"X"`
		OrderPrice    string `json:"p"`
		AvgPrice      string `json:"ap"`
		LastExecPrice string `json:"L"`
		LastExecQty   string `json:"l"`
		TradeTime     int64  `json:"T"`
		TradeID       int64  `json:"t"`
		PositionSide  string `json:"ps"`
		ReduceOnly    bool   `json:"R"`
	} `json:"o"`
}

func (c *Client) NewUserStream(ctx context.Context, keepalive time.Duration) (*UserStream, error) {
	if c.wsBaseURL == "" {
		return nil, errors.New("ws base url required")
	}
	listenKey, err := c.createListenKey(ctx)
	if err != nil {
		return nil, err
	}
	wsURL := c.userStreamWSURL(listenKey)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		_ = c.closeListenKey(context.Background(), listenKey)
		return nil, err
	}
	return &UserStream{client: c, conn: conn, keepalive: keepalive, listenKey: listenKey}, nil
}

func (c *Client) userStreamWSURL(listenKey string) string {
	base := strings.TrimRight(c.wsBaseURL, "/")
	if strings.HasSuffix(base, "/ws") {
		return base + "/" + listenKey
	}
	return base + "/ws/" + listenKey
}

func (c *Client) createListenKey(ctx context.Context) (string, error) {
	body, err := c.doRequest(ctx, http.MethodPost, "/fapi/v1/listenKey", url.Values{}, AuthAPIKey)
	if err != nil {
		return "", err
	}
	var resp listenKeyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.ListenKey) == "" {
		return "", errors.New("empty listen key")
	}
	return strings.TrimSpace(resp.ListenKey), nil
}

func (c *Client) keepaliveListenKey(ctx context.Context, listenKey string) error {
	params := url.Values{}
	params.Set("listenKey", listenKey)
	_, err := c.doRequest(ctx, http.MethodPut, "/fapi/v1/listenKey", params, AuthAPIKey)
	return err
}

func (c *Client) closeListenKey(ctx context.Context, listenKey string) error {
	params := url.Values{}
	params.Set("listenKey", listenKey)
	_, err := c.doRequest(ctx, http.MethodDelete, "/fapi/v1/listenKey", params, AuthAPIKey)
	return err
}

func (u *UserStream) Trades(ctx context.Context, symbol string) (<-chan core.Trade, <-chan error) {
	trades := make(chan core.Trade)
	errCh := make(chan error, 4)
	done := make(chan struct{})

	reportErr := func(err error) {
		if err == nil {
			return
		}
		select {
		case errCh <- err:
		default:
		}
	}

	readTimeout := 90 * time.Second
	if u.keepalive > 0 {
		readTimeout = u.keepalive * 3
		if readTimeout < 30*time.Second {
			readTimeout = 30 * time.Second
		}
	}
	u.conn.SetPongHandler(func(string) error {
		return u.conn.SetReadDeadline(time.Now().Add(readTimeout))
	})

	go func() {
		defer close(done)
		defer close(trades)
		defer u.conn.Close()
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = u.client.closeListenKey(closeCtx, u.listenKey)
		}()

		for {
			_ = u.conn.SetReadDeadline(time.Now().Add(readTimeout))
			_, data, err := u.conn.ReadMessage()
			if err != nil {
				reportErr(err)
				return
			}
			if len(data) == 0 {
				continue
			}
			if isWSResponse(data) {
				continue
			}

			var msg futuresOrderTradeUpdate
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.EventType != "ORDER_TRADE_UPDATE" {
				continue
			}
			if symbol != "" && msg.Order.Symbol != symbol {
				continue
			}
			if msg.Order.ExecutionType != "TRADE" {
				continue
			}
			qty, err := decimal.NewFromString(msg.Order.LastExecQty)
			if err != nil {
				continue
			}
			if qty.Cmp(decimal.Zero) <= 0 {
				continue
			}
			price, err := decimal.NewFromString(msg.Order.LastExecPrice)
			if err != nil || price.Cmp(decimal.Zero) <= 0 {
				price, err = decimal.NewFromString(msg.Order.AvgPrice)
				if err != nil || price.Cmp(decimal.Zero) <= 0 {
					price, err = decimal.NewFromString(msg.Order.OrderPrice)
					if err != nil || price.Cmp(decimal.Zero) <= 0 {
						continue
					}
				}
			}
			ts := msg.Order.TradeTime
			if ts == 0 {
				ts = msg.TransactionTime
			}
			if ts == 0 {
				ts = msg.EventTime
			}
			if ts == 0 {
				reportErr(errors.New("missing trade timestamp"))
				continue
			}
			tradeID := ""
			if msg.Order.TradeID > 0 {
				tradeID = strconv.FormatInt(msg.Order.TradeID, 10)
			}
			trade := core.Trade{
				OrderID:      strconv.FormatInt(msg.Order.OrderID, 10),
				TradeID:      tradeID,
				Symbol:       msg.Order.Symbol,
				Side:         core.Side(msg.Order.Side),
				PositionSide: core.PositionSide(msg.Order.PositionSide),
				ReduceOnly:   msg.Order.ReduceOnly,
				Price:        price,
				Qty:          qty,
				Status:       core.OrderStatus(msg.Order.OrderStatus),
				Time:         time.UnixMilli(ts),
			}
			select {
			case trades <- trade:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		select {
		case <-ctx.Done():
			_ = u.conn.Close()
		case <-done:
		}
	}()

	if u.keepalive > 0 {
		go func() {
			ticker := time.NewTicker(u.keepalive)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					keepaliveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					err := u.client.keepaliveListenKey(keepaliveCtx, u.listenKey)
					cancel()
					if err != nil {
						reportErr(err)
						_ = u.conn.Close()
						return
					}
					if err := u.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
						reportErr(err)
						_ = u.conn.Close()
						return
					}
				case <-done:
					return
				case <-ctx.Done():
					_ = u.conn.Close()
					return
				}
			}
		}()
	}

	return trades, errCh
}
