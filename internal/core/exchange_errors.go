package core

import "errors"

var (
	// ErrInsufficientBalance 表示交易所因资金不足而拒绝该操作。
	ErrInsufficientBalance = errors.New("insufficient balance")
	// ErrDuplicateOrder 表示该 client order id 之前已经被接受。
	ErrDuplicateOrder = errors.New("duplicate order")
	// ErrOrderNotFound 表示交易所侧不存在该订单。
	ErrOrderNotFound = errors.New("order not found")
	// ErrOrderRejected 表示订单被交易所拒绝。
	ErrOrderRejected = errors.New("order rejected")
	// ErrOrderExpired 表示订单在交易所已过期。
	ErrOrderExpired = errors.New("order expired")
)
