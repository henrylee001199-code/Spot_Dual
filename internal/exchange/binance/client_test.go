package binance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
)

func TestNormalizeClientOrderPrefix(t *testing.T) {
	if got := normalizeClientOrderPrefix(" BOT_A1 "); got != "bot_a1" {
		t.Fatalf("normalizeClientOrderPrefix() = %q, want %q", got, "bot_a1")
	}
	if got := normalizeClientOrderPrefix("!!!"); got != "gt" {
		t.Fatalf("normalizeClientOrderPrefix() = %q, want %q", got, "gt")
	}
	long := strings.Repeat("a", 30)
	got := normalizeClientOrderPrefix(long)
	if len(got) != 20 {
		t.Fatalf("normalizeClientOrderPrefix(long) len = %d, want 20", len(got))
	}
}

func TestParseAPIError(t *testing.T) {
	err := parseAPIError(http.StatusBadRequest, []byte(`{"code":-2010,"msg":"Duplicate order sent."}`))
	apiErr, ok := err.(APIError)
	if !ok {
		t.Fatalf("parseAPIError() type = %T, want APIError", err)
	}
	if apiErr.Code != -2010 {
		t.Fatalf("apiErr.Code = %d, want -2010", apiErr.Code)
	}
	if apiErr.Msg != "Duplicate order sent." {
		t.Fatalf("apiErr.Msg = %q, want %q", apiErr.Msg, "Duplicate order sent.")
	}

	err = parseAPIError(http.StatusBadGateway, []byte("bad gateway"))
	if strings.Contains(err.Error(), "binance api error") {
		t.Fatalf("parseAPIError(non-json) unexpectedly returned APIError: %v", err)
	}
	if !strings.Contains(err.Error(), "http error 502") {
		t.Fatalf("parseAPIError(non-json) = %v, want http error", err)
	}
}

func TestParseSymbolInfo(t *testing.T) {
	src := symbolInfoResponse{
		Symbol:     "BTCUSDT",
		BaseAsset:  "BTC",
		QuoteAsset: "USDT",
		Filters: []struct {
			FilterType  string `json:"filterType"`
			MinQty      string `json:"minQty"`
			StepSize    string `json:"stepSize"`
			MinNotional string `json:"minNotional"`
			TickSize    string `json:"tickSize"`
		}{
			{FilterType: "LOT_SIZE", MinQty: "0.0001", StepSize: "0.0001"},
			{FilterType: "PRICE_FILTER", TickSize: "0.01"},
			{FilterType: "MIN_NOTIONAL", MinNotional: "5"},
		},
	}
	info := parseSymbolInfo(src)
	if info.baseAsset != "BTC" || info.quoteAsset != "USDT" {
		t.Fatalf("assets = %s/%s, want BTC/USDT", info.baseAsset, info.quoteAsset)
	}
	if !info.rules.MinQty.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("MinQty = %s, want 0.0001", info.rules.MinQty)
	}
	if !info.rules.QtyStep.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("QtyStep = %s, want 0.0001", info.rules.QtyStep)
	}
	if !info.rules.PriceTick.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("PriceTick = %s, want 0.01", info.rules.PriceTick)
	}
	if !info.rules.MinNotional.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("MinNotional = %s, want 5", info.rules.MinNotional)
	}
}

func TestWSOrderParamsSignatureAndSession(t *testing.T) {
	sigClient := NewClientWithOptions(Options{
		APIKey:         "k",
		APISecret:      "s",
		UserStreamAuth: "signature",
		RecvWindowMs:   5000,
	})
	order := core.Order{
		Symbol:   "BTCUSDT",
		Side:     core.Buy,
		Type:     core.Limit,
		Price:    decimal.RequireFromString("100"),
		Qty:      decimal.RequireFromString("0.01"),
		ClientID: "cid-1",
	}
	params, err := sigClient.wsOrderParams(order)
	if err != nil {
		t.Fatalf("wsOrderParams(signature) error = %v", err)
	}
	if params["apiKey"] != "k" {
		t.Fatalf("apiKey param = %v, want k", params["apiKey"])
	}
	if _, ok := params["signature"].(string); !ok {
		t.Fatalf("signature param missing or invalid: %v", params["signature"])
	}

	sessionClient := NewClientWithOptions(Options{
		UserStreamAuth: "session",
		RecvWindowMs:   5000,
	})
	params, err = sessionClient.wsOrderParams(order)
	if err != nil {
		t.Fatalf("wsOrderParams(session) error = %v", err)
	}
	if _, ok := params["apiKey"]; ok {
		t.Fatalf("session wsOrderParams should not include apiKey")
	}
	if _, ok := params["signature"]; ok {
		t.Fatalf("session wsOrderParams should not include signature")
	}
}

func TestPlaceOrderRESTDuplicateFallbackByClientID(t *testing.T) {
	var postCalls int32
	var getCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/order" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodPost:
			atomic.AddInt32(&postCalls, 1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":-2010,"msg":"Duplicate order sent."}`))
		case http.MethodGet:
			atomic.AddInt32(&getCalls, 1)
			if r.URL.Query().Get("origClientOrderId") != "cid-dup" {
				t.Fatalf("origClientOrderId = %q, want %q", r.URL.Query().Get("origClientOrderId"), "cid-dup")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"symbol":              "BTCUSDT",
				"orderId":             123456,
				"clientOrderId":       "cid-dup",
				"price":               "100",
				"origQty":             "0.01",
				"executedQty":         "0",
				"cummulativeQuoteQty": "0",
				"status":              "NEW",
				"side":                "BUY",
				"type":                "LIMIT",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClientWithOptions(Options{
		APIKey:      "k",
		APISecret:   "s",
		RestBaseURL: srv.URL,
		WSBaseURL:   "ws://unused",
	})

	order := core.Order{
		Symbol:   "BTCUSDT",
		Side:     core.Buy,
		Type:     core.Limit,
		Price:    decimal.RequireFromString("100"),
		Qty:      decimal.RequireFromString("0.01"),
		ClientID: "cid-dup",
	}
	got, err := c.placeOrderREST(context.Background(), order)
	if err != nil {
		t.Fatalf("placeOrderREST() error = %v", err)
	}
	if got.ID != "123456" {
		t.Fatalf("order id = %q, want 123456", got.ID)
	}
	if got.ClientID != "cid-dup" {
		t.Fatalf("client id = %q, want cid-dup", got.ClientID)
	}
	if atomic.LoadInt32(&postCalls) != 1 || atomic.LoadInt32(&getCalls) != 1 {
		t.Fatalf("calls post/get = %d/%d, want 1/1", postCalls, getCalls)
	}
}

func TestPlaceOrderFallsBackToRESTWhenWSUnavailable(t *testing.T) {
	var postCalls int32
	var seenClientID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/order" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&postCalls, 1)
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		seenClientID = values.Get("newClientOrderId")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"symbol":  "BTCUSDT",
			"orderId": 777,
			"price":   "100",
			"origQty": "0.01",
		})
	}))
	defer srv.Close()

	c := NewClientWithOptions(Options{
		APIKey:      "k",
		APISecret:   "s",
		RestBaseURL: srv.URL,
		WSBaseURL:   "",
	})

	order := core.Order{
		Symbol: "BTCUSDT",
		Side:   core.Buy,
		Type:   core.Limit,
		Price:  decimal.RequireFromString("100"),
		Qty:    decimal.RequireFromString("0.01"),
	}
	got, err := c.PlaceOrder(context.Background(), order)
	if err != nil {
		t.Fatalf("PlaceOrder() error = %v", err)
	}
	if got.ID != "777" {
		t.Fatalf("order id = %q, want 777", got.ID)
	}
	if seenClientID == "" {
		t.Fatalf("newClientOrderId should be auto generated")
	}
	if atomic.LoadInt32(&postCalls) != 1 {
		t.Fatalf("post calls = %d, want 1", postCalls)
	}
}
