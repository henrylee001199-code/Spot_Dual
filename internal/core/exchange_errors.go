package core

import "errors"

var (
	// ErrInsufficientBalance indicates the exchange rejected the action due to insufficient funds.
	ErrInsufficientBalance = errors.New("insufficient balance")
	// ErrDuplicateOrder indicates the client order id has already been accepted before.
	ErrDuplicateOrder = errors.New("duplicate order")
	// ErrOrderNotFound indicates the order does not exist on exchange.
	ErrOrderNotFound = errors.New("order not found")
	// ErrOrderRejected indicates the order was rejected by exchange.
	ErrOrderRejected = errors.New("order rejected")
	// ErrOrderExpired indicates the order has expired on exchange.
	ErrOrderExpired = errors.New("order expired")
)
