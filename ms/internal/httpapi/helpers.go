package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/cecinuga/pff/ms/internal/analytics"
	"github.com/cecinuga/pff/ms/internal/auth"
	"github.com/cecinuga/pff/ms/internal/bank"
	"github.com/cecinuga/pff/ms/internal/budget"
	"github.com/cecinuga/pff/ms/internal/connect"
	"github.com/cecinuga/pff/ms/internal/domain"
	"github.com/cecinuga/pff/ms/internal/platform/httperr"
	"github.com/cecinuga/pff/ms/internal/warehouse"
)

// fail maps service errors to HTTP responses. Unknown errors are logged and
// reported as a generic 500 so internals never leak to the client.
func (a *api) fail(c *gin.Context, err error) {
	var ve budget.ValidationError
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe):
		httperr.Abort(c, http.StatusRequestEntityTooLarge, "body_too_large", "request body too large")
	case errors.Is(err, domain.ErrInvalidRange):
		httperr.Abort(c, http.StatusBadRequest, "invalid_range", err.Error())
	case errors.As(err, &ve):
		httperr.Abort(c, http.StatusBadRequest, "invalid_input", ve.Msg)
	case errors.Is(err, analytics.ErrBadCursor):
		httperr.Abort(c, http.StatusBadRequest, "invalid_cursor", err.Error())
	case errors.Is(err, analytics.ErrNotFound), errors.Is(err, warehouse.ErrNotFound),
		errors.Is(err, budget.ErrNotFound), errors.Is(err, connect.ErrNotFound):
		httperr.Abort(c, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, connect.ErrUnknownBank):
		httperr.Abort(c, http.StatusBadRequest, "unknown_bank", err.Error())
	case errors.Is(err, connect.ErrBadState):
		httperr.Abort(c, http.StatusBadRequest, "invalid_state", err.Error())
	case errors.Is(err, connect.ErrBankRejected):
		httperr.Abort(c, http.StatusBadRequest, "bank_rejected", err.Error())
	case errors.Is(err, auth.ErrOTPRateLimited):
		httperr.Abort(c, http.StatusTooManyRequests, "rate_limited", err.Error())
	case errors.Is(err, auth.ErrOTPInvalid):
		httperr.Abort(c, http.StatusBadRequest, "invalid_code", err.Error())
	case errors.Is(err, auth.ErrOTPNotEnrolled), errors.Is(err, auth.ErrOTPAlreadyActive):
		httperr.Abort(c, http.StatusConflict, "mfa_state", err.Error())
	case errors.Is(err, auth.ErrOTPNeedsMFA):
		httperr.Abort(c, http.StatusForbidden, auth.MFAStateRequired, err.Error())
	case errors.Is(err, bank.ErrRateLimited):
		httperr.Abort(c, http.StatusServiceUnavailable, "bank_rate_limited", "the bank is rate-limiting requests, try again later")
	case errors.Is(err, bank.ErrUnavailable):
		httperr.Abort(c, http.StatusBadGateway, "bank_unavailable", "the bank provider is temporarily unavailable")
	default:
		var be *bank.Error
		if errors.As(err, &be) {
			a.Log.Warn("bank provider error", "status", be.Status, "code", be.Code)
			httperr.Abort(c, http.StatusBadGateway, "bank_error", "the bank provider rejected the request")
			return
		}
		a.Log.Error("request failed", "route", c.FullPath(), "err", err)
		httperr.Abort(c, http.StatusInternalServerError, "internal", "internal error")
	}
}

func badRequest(c *gin.Context, msg string) {
	httperr.Abort(c, http.StatusBadRequest, "invalid_input", msg)
}

// bindJSON decodes the body into v, answering 400 on failure.
func (a *api) bindJSON(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			a.fail(c, err)
			return false
		}
		badRequest(c, "malformed JSON body")
		return false
	}
	return true
}

// psu captures the user's request headers for the bank (PSD2 "PSU present").
func psu(c *gin.Context) *bank.PSU {
	h := c.Request.Header
	return &bank.PSU{
		IPAddress:      c.ClientIP(),
		UserAgent:      h.Get("User-Agent"),
		Referer:        h.Get("Referer"),
		Accept:         h.Get("Accept"),
		AcceptCharset:  h.Get("Accept-Charset"),
		AcceptEncoding: h.Get("Accept-Encoding"),
		AcceptLanguage: h.Get("Accept-Language"),
	}
}

// parseRange resolves the range, from, to and end query parameters.
func (a *api) parseRange(c *gin.Context) (domain.DateRange, bool) {
	r, err := domain.ResolveRange(domain.RangeRequest{
		Code: c.Query("range"),
		From: c.Query("from"),
		To:   c.Query("to"),
		End:  c.Query("end"),
	}, a.Ingest.Today())
	if err != nil {
		a.fail(c, err)
		return r, false
	}
	return r, true
}

// parseDomain reads the domain query parameter.
func parseDomain(c *gin.Context) (domain.MetricDomain, bool) {
	d, err := domain.ParseDomain(c.Query("domain"))
	if err != nil {
		badRequest(c, "domain must be one of spending, spending_category, income, bank_transfer")
		return "", false
	}
	return d, true
}

// parseClass reads the optional class query parameter of domain d.
func parseClass(c *gin.Context, d domain.MetricDomain) (domain.MetricClass, bool) {
	cl := domain.MetricClass(c.Query("class"))
	if cl != "" && !d.HasClass(cl) {
		badRequest(c, "class does not belong to the domain")
		return "", false
	}
	return cl, true
}

func queryInt(c *gin.Context, key string, def int) int {
	if v, err := strconv.Atoi(c.Query(key)); err == nil {
		return v
	}
	return def
}
