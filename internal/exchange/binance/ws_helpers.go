package binance

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

type wsRequest struct {
	ID     string                 `json:"id"`
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params,omitempty"`
}

type wsResponse struct {
	ID     string          `json:"id"`
	Status int             `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *apiError       `json:"error,omitempty"`
}

func sendWSRequest(ctx context.Context, conn *websocket.Conn, method string, params map[string]interface{}) (wsResponse, error) {
	reqID := strconv.FormatInt(time.Now().UnixNano(), 10)
	req := wsRequest{
		ID:     reqID,
		Method: method,
		Params: params,
	}
	if err := conn.WriteJSON(req); err != nil {
		return wsResponse{}, err
	}
	return waitForWSResponse(ctx, conn, reqID)
}

func waitForWSResponse(ctx context.Context, conn *websocket.Conn, reqID string) (wsResponse, error) {
	deadline := time.Now().Add(10 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetReadDeadline(deadline)
	defer conn.SetReadDeadline(time.Time{})

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return wsResponse{}, err
		}
		var resp wsResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		if resp.ID == "" {
			continue
		}
		if resp.ID != reqID {
			continue
		}
		if resp.Status != 200 {
			if resp.Error != nil {
				return resp, fmt.Errorf("binance ws error %d: %s", resp.Error.Code, resp.Error.Msg)
			}
			return resp, fmt.Errorf("binance ws error status %d", resp.Status)
		}
		return resp, nil
	}
}

func isWSResponse(data []byte) bool {
	var resp wsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return false
	}
	return resp.ID != ""
}

func signEd25519(payload string, key ed25519.PrivateKey) string {
	signature := ed25519.Sign(key, []byte(payload))
	return base64.StdEncoding.EncodeToString(signature)
}
