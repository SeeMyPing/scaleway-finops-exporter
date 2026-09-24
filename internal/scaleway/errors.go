package scaleway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/scaleway/scaleway-sdk-go/scw"
)

// Error reasons, used as the "reason" label of scaleway_exporter_source_errors_total.
// The set is closed to keep the label cardinality bounded.
const (
	ReasonTimeout     = "timeout"
	ReasonCanceled    = "canceled"
	ReasonAuth        = "auth"
	ReasonRateLimited = "rate_limited"
	ReasonServer      = "server"
	ReasonClient      = "client"
	ReasonDecode      = "decode"
	ReasonNetwork     = "network"
	ReasonUnknown     = "unknown"
)

// Classify maps an error returned by the SDK to one of the Reason constants.
func Classify(err error) string {
	var (
		responseErr  *scw.ResponseError
		deniedErr    *scw.DeniedAuthenticationError
		permsErr     *scw.PermissionsDeniedError
		syntaxErr    *json.SyntaxError
		typeErr      *json.UnmarshalTypeError
		netErr       net.Error
		invalidArgs  *scw.InvalidArgumentsError
		notFoundErr  *scw.ResourceNotFoundError
		quotasErr    *scw.QuotasExceededError
		preconditErr *scw.PreconditionFailedError
	)

	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonTimeout
	case errors.Is(err, context.Canceled):
		return ReasonCanceled
	case errors.As(err, &deniedErr), errors.As(err, &permsErr):
		return ReasonAuth
	case errors.As(err, &invalidArgs), errors.As(err, &notFoundErr),
		errors.As(err, &quotasErr), errors.As(err, &preconditErr):
		return ReasonClient
	case errors.As(err, &responseErr):
		return classifyStatus(responseErr.StatusCode)
	case errors.As(err, &syntaxErr), errors.As(err, &typeErr), errors.Is(err, io.ErrUnexpectedEOF):
		return ReasonDecode
	case errors.As(err, &netErr):
		if netErr.Timeout() {
			return ReasonTimeout
		}
		return ReasonNetwork
	default:
		return ReasonUnknown
	}
}

func classifyStatus(code int) string {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return ReasonAuth
	case code == http.StatusTooManyRequests:
		return ReasonRateLimited
	case code >= 500:
		return ReasonServer
	case code >= 400:
		return ReasonClient
	default:
		return ReasonUnknown
	}
}
