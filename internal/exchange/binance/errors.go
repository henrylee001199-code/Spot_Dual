package binance

import (
	"errors"
	"strings"

	"spot-dual/internal/core"
)

const (
	apiCodeNewOrderRejected = -2010
	apiCodeCancelRejected   = -2011
	apiCodeOrderNotFound    = -2013
)

var apiErrorMessageKinds = map[string]error{
	"duplicate order sent.":                                  core.ErrDuplicateOrder,
	"account has insufficient balance for requested action.": core.ErrInsufficientBalance,
	"balance is insufficient.":                               core.ErrInsufficientBalance,
	"unknown order sent.":                                    core.ErrOrderNotFound,
	"order does not exist.":                                  core.ErrOrderNotFound,
	"order was canceled or expired.":                         core.ErrOrderExpired,
}

func wrapAPIError(code int, msg string) error {
	return classifyAPIError(APIError{Code: code, Msg: msg})
}

func classifyAPIError(apiErr APIError) error {
	kinds := classifyAPIErrorKinds(apiErr)
	if len(kinds) == 0 {
		return apiErr
	}
	errChain := make([]error, 0, 1+len(kinds))
	errChain = append(errChain, apiErr)
	errChain = append(errChain, kinds...)
	return errors.Join(errChain...)
}

func classifyAPIErrorKinds(apiErr APIError) []error {
	kinds := make([]error, 0, 2)
	normalizedMsg := normalizeAPIErrorMsg(apiErr.Msg)

	switch apiErr.Code {
	case apiCodeOrderNotFound, apiCodeCancelRejected:
		kinds = appendErrorKind(kinds, core.ErrOrderNotFound)
	case apiCodeNewOrderRejected:
		if kind, ok := apiErrorMessageKinds[normalizedMsg]; ok {
			kinds = appendErrorKind(kinds, kind)
		} else {
			kinds = appendErrorKind(kinds, core.ErrOrderRejected)
		}
	}

	if kind, ok := apiErrorMessageKinds[normalizedMsg]; ok {
		kinds = appendErrorKind(kinds, kind)
	}

	return kinds
}

func appendErrorKind(kinds []error, kind error) []error {
	if kind == nil {
		return kinds
	}
	for _, existing := range kinds {
		if existing == kind {
			return kinds
		}
	}
	return append(kinds, kind)
}

func normalizeAPIErrorMsg(msg string) string {
	return strings.ToLower(strings.TrimSpace(msg))
}

func AsAPIError(err error) (APIError, bool) {
	if err == nil {
		return APIError{}, false
	}
	var apiErr APIError
	if !errors.As(err, &apiErr) {
		return APIError{}, false
	}
	return apiErr, true
}

func IsAPIErrorCode(err error, codes ...int) bool {
	apiErr, ok := AsAPIError(err)
	if !ok {
		return false
	}
	for _, code := range codes {
		if apiErr.Code == code {
			return true
		}
	}
	return false
}
