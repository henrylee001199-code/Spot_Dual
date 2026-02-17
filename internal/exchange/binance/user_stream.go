package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
)

type UserStream struct {
	client    *Client
	conn      *websocket.Conn
	keepalive time.Duration
}

type executionReport struct {
	EventType       string `json:"e"`
	EventTime       int64  `json:"E"`
	Symbol          string `json:"s"`
	OrderID         int64  `json:"i"`
	Side            string `json:"S"`
	ExecutionType   string `json:"x"`
	OrderStatus     string `json:"X"`
	OrderPrice      string `json:"p"`
	OrderQty        string `json:"q"`
	LastExecPrice   string `json:"L"`
	LastExecQty     string `json:"l"`
	CumulativeQty   string `json:"z"`
	TransactionTime int64  `json:"T"`
	TradeID         int64  `json:"t"`
}

func (c *Client) NewUserStream(ctx context.Context, keepalive time.Duration) (*UserStream, error) {
	if c.wsBaseURL == "" {
		return nil, errors.New("ws base url required")
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
		if _, err := sendWSRequest(ctx, conn, "userDataStream.subscribe", nil); err != nil {
			_ = conn.Close()
			return nil, err
		}
	} else {
		params, err := c.userStreamParams()
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if _, err := sendWSRequest(ctx, conn, "userDataStream.subscribe.signature", params); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return &UserStream{client: c, conn: conn, keepalive: keepalive}, nil
}

func (c *Client) userStreamParams() (map[string]interface{}, error) {
	if c.apiKey == "" || c.apiSecret == "" {
		return nil, errors.New("api_key/api_secret required")
	}
	if c.userStreamAuth != "signature" {
		return nil, fmt.Errorf("unsupported user stream auth: %s", c.userStreamAuth)
	}
	ts := time.Now().UnixMilli()
	values := url.Values{}
	values.Set("apiKey", c.apiKey)
	values.Set("timestamp", strconv.FormatInt(ts, 10))
	if c.recvWindow > 0 {
		values.Set("recvWindow", strconv.FormatInt(c.recvWindow.Milliseconds(), 10))
	}
	signature := sign(c.apiSecret, values.Encode())
	params := map[string]interface{}{
		"apiKey":    c.apiKey,
		"timestamp": ts,
		"signature": signature,
	}
	if c.recvWindow > 0 {
		params["recvWindow"] = c.recvWindow.Milliseconds()
	}
	return params, nil
}

func (c *Client) sessionLogon(ctx context.Context, conn *websocket.Conn) error {
	params, err := c.sessionLogonParams()
	if err != nil {
		return err
	}
	_, err = sendWSRequest(ctx, conn, "session.logon", params)
	return err
}

func (c *Client) sessionLogonParams() (map[string]interface{}, error) {
	if c.apiKey == "" {
		return nil, errors.New("api_key required")
	}
	if c.wsEd25519Key == nil {
		return nil, errors.New("ed25519 key not loaded")
	}
	ts := time.Now().UnixMilli()
	values := url.Values{}
	values.Set("apiKey", c.apiKey)
	values.Set("timestamp", strconv.FormatInt(ts, 10))
	if c.recvWindow > 0 {
		values.Set("recvWindow", strconv.FormatInt(c.recvWindow.Milliseconds(), 10))
	}
	signature := signEd25519(values.Encode(), c.wsEd25519Key)
	params := map[string]interface{}{
		"apiKey":    c.apiKey,
		"timestamp": ts,
		"signature": signature,
	}
	if c.recvWindow > 0 {
		params["recvWindow"] = c.recvWindow.Milliseconds()
	}
	return params, nil
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

	readTimeout := 45 * time.Second
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
			var msg executionReport
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.EventType != "executionReport" {
				continue
			}
			if symbol != "" && msg.Symbol != symbol {
				continue
			}
			if msg.ExecutionType != "TRADE" {
				continue
			}
			qty, err := decimal.NewFromString(msg.LastExecQty)
			if err != nil {
				continue
			}
			if qty.Cmp(decimal.Zero) <= 0 {
				continue
			}
			price, err := decimal.NewFromString(msg.LastExecPrice)
			if err != nil {
				price, err = decimal.NewFromString(msg.OrderPrice)
				if err != nil {
					continue
				}
			}
			if price.Cmp(decimal.Zero) <= 0 {
				continue
			}
			ts := msg.TransactionTime
			if ts == 0 {
				ts = msg.EventTime
			}
			if ts == 0 {
				reportErr(errors.New("missing trade timestamp"))
				continue
			}
			tradeID := ""
			if msg.TradeID > 0 {
				tradeID = strconv.FormatInt(msg.TradeID, 10)
			}
			trade := core.Trade{
				OrderID: strconv.FormatInt(msg.OrderID, 10),
				TradeID: tradeID,
				Symbol:  msg.Symbol,
				Side:    core.Side(msg.Side),
				Price:   price,
				Qty:     qty,
				Status:  core.OrderStatus(msg.OrderStatus),
				Time:    time.UnixMilli(ts),
			}
			select {
			case trades <- trade:
			case <-ctx.Done():
				return
			}
		}
	}()

	if u.keepalive > 0 {
		go func() {
			ticker := time.NewTicker(u.keepalive)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
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
