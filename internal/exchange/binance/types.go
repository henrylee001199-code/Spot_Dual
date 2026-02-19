package binance

import (
	"strconv"

	"github.com/shopspring/decimal"

	"grid-trading/internal/core"
)

type apiError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

type APIError struct {
	Code int
	Msg  string
}

func (e APIError) Error() string {
	return "binance api error " + strconv.Itoa(e.Code) + ": " + e.Msg
}

type orderResponse struct {
	Symbol  string `json:"symbol"`
	OrderID int64  `json:"orderId"`
	Price   string `json:"price"`
	OrigQty string `json:"origQty"`
}

type orderQueryResponse struct {
	Symbol             string `json:"symbol"`
	OrderID            int64  `json:"orderId"`
	ClientOrderID      string `json:"clientOrderId"`
	Price              string `json:"price"`
	OrigQty            string `json:"origQty"`
	ExecutedQty        string `json:"executedQty"`
	CumulativeQuoteQty string `json:"cummulativeQuoteQty"`
	Status             string `json:"status"`
	Side               string `json:"side"`
	Type               string `json:"type"`
	Time               int64  `json:"time"`
	UpdateTime         int64  `json:"updateTime"`
}

type openOrderResponse struct {
	Symbol      string `json:"symbol"`
	OrderID     int64  `json:"orderId"`
	Price       string `json:"price"`
	OrigQty     string `json:"origQty"`
	ExecutedQty string `json:"executedQty"`
	Side        string `json:"side"`
	Type        string `json:"type"`
}

type tickerPriceResponse struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

type accountResponse struct {
	Balances []struct {
		Asset  string `json:"asset"`
		Free   string `json:"free"`
		Locked string `json:"locked"`
	} `json:"balances"`
}

type exchangeInfoResponse struct {
	Symbols []symbolInfoResponse `json:"symbols"`
}

type symbolInfoResponse struct {
	Symbol     string `json:"symbol"`
	BaseAsset  string `json:"baseAsset"`
	QuoteAsset string `json:"quoteAsset"`
	Filters    []struct {
		FilterType  string `json:"filterType"`
		MinQty      string `json:"minQty"`
		StepSize    string `json:"stepSize"`
		MinNotional string `json:"minNotional"`
		TickSize    string `json:"tickSize"`
	} `json:"filters"`
}

type symbolInfo struct {
	baseAsset  string
	quoteAsset string
	rules      core.Rules
}

func parseSymbolInfo(src symbolInfoResponse) symbolInfo {
	info := symbolInfo{
		baseAsset:  src.BaseAsset,
		quoteAsset: src.QuoteAsset,
		rules:      core.Rules{MinQty: decimal.Zero, MinNotional: decimal.Zero, PriceTick: decimal.Zero, QtyStep: decimal.Zero},
	}
	for _, f := range src.Filters {
		switch f.FilterType {
		case "LOT_SIZE":
			if f.MinQty != "" {
				if v, err := decimal.NewFromString(f.MinQty); err == nil {
					info.rules.MinQty = v
				}
			}
			if f.StepSize != "" {
				if v, err := decimal.NewFromString(f.StepSize); err == nil {
					info.rules.QtyStep = v
				}
			}
		case "PRICE_FILTER":
			if f.TickSize != "" {
				if v, err := decimal.NewFromString(f.TickSize); err == nil {
					info.rules.PriceTick = v
				}
			}
		case "MIN_NOTIONAL", "NOTIONAL":
			if f.MinNotional != "" {
				if v, err := decimal.NewFromString(f.MinNotional); err == nil {
					// If both MIN_NOTIONAL and NOTIONAL are present, keep the stricter minimum.
					if v.Cmp(info.rules.MinNotional) > 0 {
						info.rules.MinNotional = v
					}
				}
			}
		}
	}
	return info
}
