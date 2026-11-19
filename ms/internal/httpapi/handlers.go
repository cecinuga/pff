package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/cecinuga/pff/ms/internal/analytics"
	"github.com/cecinuga/pff/ms/internal/auth"
	"github.com/cecinuga/pff/ms/internal/budget"
	"github.com/cecinuga/pff/ms/internal/connect"
	"github.com/cecinuga/pff/ms/internal/domain"
	"github.com/cecinuga/pff/ms/internal/ingest"
)

// ---- public -------------------------------------------------------------------

// publicConfig hands the web app its runtime configuration, so one client build
// works in every environment. Everything here is public by design (Firebase
// web config is not a secret).
func (a *api) publicConfig(c *gin.Context) {
	f := a.Cfg.Firebase
	c.JSON(http.StatusOK, gin.H{
		"auth_mode":   a.Cfg.Auth.Mode,
		"require_mfa": a.Cfg.Auth.RequireMFA,
		"firebase": gin.H{
			"apiKey":            f.WebAPIKey,
			"authDomain":        f.AuthDomain,
			"projectId":         f.ProjectID,
			"appId":             f.AppID,
			"messagingSenderId": f.MessagingSenderID,
		},
		"vapid_key":     f.VAPIDKey,
		"push_enabled":  a.Cfg.Push.Provider == "fcm" && f.VAPIDKey != "",
		"bank_provider": a.Cfg.Bank.Provider,
	})
}

// ---- identity & MFA -----------------------------------------------------------

func (a *api) me(c *gin.Context) {
	p := auth.PrincipalFrom(c)
	c.JSON(http.StatusOK, gin.H{
		"user": p.User,
		"mfa": gin.H{
			"state":              p.MFAState,
			"method":             p.MFAMethod,
			"email_enabled":      p.User.EmailMFAEnabled,
			"firebase_factor":    p.Identity.SecondFactor,
			"required_by_policy": a.Cfg.Auth.RequireMFA,
		},
	})
}

func (a *api) emailChallenge(c *gin.Context) {
	var body struct {
		Purpose string `json:"purpose"`
	}
	if !a.bindJSON(c, &body) {
		return
	}
	if body.Purpose != auth.PurposeLogin && body.Purpose != auth.PurposeEnroll {
		badRequest(c, "purpose must be login or enroll")
		return
	}
	id, expires, err := a.OTP.Challenge(c.Request.Context(), auth.PrincipalFrom(c), body.Purpose)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"challenge_id": id, "expires_at": expires})
}

func (a *api) emailVerify(c *gin.Context) {
	var body struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}
	if !a.bindJSON(c, &body) {
		return
	}
	code := strings.TrimSpace(body.Code)
	if body.ChallengeID == "" || len(code) != 6 {
		badRequest(c, "challenge_id and a 6-digit code are required")
		return
	}
	if err := a.OTP.Verify(c.Request.Context(), auth.PrincipalFrom(c), body.ChallengeID, code); err != nil {
		a.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *api) emailDisable(c *gin.Context) {
	if err := a.OTP.Disable(c.Request.Context(), auth.PrincipalFrom(c)); err != nil {
		a.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- metadata -------------------------------------------------------------------

func (a *api) meta(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"domains": domain.Catalog,
		"ranges":  domain.Ranges,
		"metrics": []gin.H{
			{"key": "mean", "label": "Mean"},
			{"key": "total", "label": "Total"},
			{"key": "max_min", "label": "Max-Min"},
		},
		"currency": a.Cfg.ReportingCurrency,
		"timezone": a.Cfg.Location.String(),
		"today":    a.Ingest.Today().Format(domain.DateLayout),
	})
}

// ---- bank connections -------------------------------------------------------------

// banks lists the banks configured in PFF_BANKS (the user cannot pick others).
func (a *api) banks(c *gin.Context) {
	banks, err := a.Connect.ConfiguredBanks(c.Request.Context())
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"banks": banks})
}

func (a *api) listConnections(c *gin.Context) {
	conns, err := a.Connect.List(c.Request.Context(), auth.PrincipalFrom(c).User.ID)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"connections": conns})
}

func (a *api) startConnection(c *gin.Context) {
	var body struct {
		ASPSPName    string `json:"aspsp_name"`
		ASPSPCountry string `json:"aspsp_country"`
	}
	if !a.bindJSON(c, &body) {
		return
	}
	if body.ASPSPName == "" || body.ASPSPCountry == "" {
		badRequest(c, "aspsp_name and aspsp_country are required")
		return
	}
	lang := ""
	if al := c.GetHeader("Accept-Language"); len(al) >= 2 {
		lang = strings.ToLower(al[:2])
	}
	url, err := a.Connect.Start(c.Request.Context(), auth.PrincipalFrom(c).User.ID, connect.StartRequest{
		ASPSPName: body.ASPSPName, ASPSPCountry: body.ASPSPCountry, Language: lang,
	})
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"redirect_url": url})
}

func (a *api) completeConnection(c *gin.Context) {
	var body struct {
		Code             string `json:"code"`
		State            string `json:"state"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if !a.bindJSON(c, &body) {
		return
	}
	id, err := a.Connect.Complete(c.Request.Context(), auth.PrincipalFrom(c).User.ID, connect.CompleteRequest{
		Code: body.Code, State: body.State, Error: body.Error, ErrorDescription: body.ErrorDescription,
	}, psu(c))
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"connection_id": id})
}

func (a *api) revokeConnection(c *gin.Context) {
	if err := a.Connect.Revoke(c.Request.Context(), auth.PrincipalFrom(c).User.ID, c.Param("id")); err != nil {
		a.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- analytics ----------------------------------------------------------------------

// ensure runs the GetOrSet cache check for the range before reading.
func (a *api) ensure(c *gin.Context, r domain.DateRange) ingest.Freshness {
	return a.Ingest.EnsureRange(c.Request.Context(), auth.PrincipalFrom(c).User.ID, r.From, r.To, psu(c))
}

// metrics returns the metrics of one domain, or of every domain when the
// domain parameter is omitted (dashboard overview).
func (a *api) metrics(c *gin.Context) {
	r, ok := a.parseRange(c)
	if !ok {
		return
	}
	userID := auth.PrincipalFrom(c).User.ID
	if c.Query("domain") == "" {
		fr := a.ensure(c, r)
		all := make([]domain.MetricDomain, len(domain.Catalog))
		for i, info := range domain.Catalog {
			all[i] = info.Domain
		}
		out, err := a.Analytics.MetricsOf(c.Request.Context(), userID, all, r)
		if err != nil {
			a.fail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"range": analytics.NewRangeDTO(r), "domains": out, "freshness": fr})
		return
	}
	d, ok := parseDomain(c)
	if !ok {
		return
	}
	fr := a.ensure(c, r)
	m, err := a.Analytics.Metrics(c.Request.Context(), userID, d, r)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"metrics": m, "freshness": fr})
}

func (a *api) allocation(c *gin.Context) {
	d, ok := parseDomain(c)
	if !ok {
		return
	}
	r, ok := a.parseRange(c)
	if !ok {
		return
	}
	fr := a.ensure(c, r)
	m, err := a.Analytics.Metrics(c.Request.Context(), auth.PrincipalFrom(c).User.ID, d, r)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"allocation": analytics.AllocationOf(m), "freshness": fr})
}

func (a *api) history(c *gin.Context) {
	d, ok := parseDomain(c)
	if !ok {
		return
	}
	class, ok := parseClass(c, d)
	if !ok {
		return
	}
	r, ok := a.parseRange(c)
	if !ok {
		return
	}
	var fr ingest.Freshness
	if c.Query("cursor") == "" { // later pages reuse the first page's check
		fr = a.ensure(c, r)
	} else {
		fr = ingest.Freshness{Complete: true, Warnings: []string{}}
	}
	page, err := a.Analytics.History(c.Request.Context(), auth.PrincipalFrom(c).User.ID, d, r, class, c.Query("cursor"), queryInt(c, "limit", 50))
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"history": page, "freshness": fr})
}

func (a *api) series(c *gin.Context) {
	d, ok := parseDomain(c)
	if !ok {
		return
	}
	class, ok := parseClass(c, d)
	if !ok {
		return
	}
	r, ok := a.parseRange(c)
	if !ok {
		return
	}
	bucket := c.Query("bucket")
	if bucket == "" {
		bucket = analytics.AutoBucket(r)
	}
	if bucket != analytics.BucketDay && bucket != analytics.BucketWeek && bucket != analytics.BucketMonth {
		badRequest(c, "bucket must be day, week or month")
		return
	}
	fr := a.ensure(c, r)
	s, err := a.Analytics.SeriesOf(c.Request.Context(), auth.PrincipalFrom(c).User.ID, d, r, class, bucket)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"series": s, "freshness": fr})
}

func (a *api) transaction(c *gin.Context) {
	e, err := a.Analytics.Entry(c.Request.Context(), auth.PrincipalFrom(c).User.ID, c.Param("id"))
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"transaction": e})
}

func (a *api) setCategory(c *gin.Context) {
	var body struct {
		Category string `json:"category"` // empty string clears the override
	}
	if !a.bindJSON(c, &body) {
		return
	}
	class := domain.MetricClass(body.Category)
	if class != "" && !domain.DomainSpendingCategory.HasClass(class) {
		badRequest(c, "unknown spending category")
		return
	}
	userID := auth.PrincipalFrom(c).User.ID
	if err := a.Warehouse.SetCategory(c.Request.Context(), userID, c.Param("id"), class); err != nil {
		a.fail(c, err)
		return
	}
	go a.Budgets.EvaluateUser(context.WithoutCancel(c.Request.Context()), userID)
	a.transaction(c)
}

// ---- budgets ------------------------------------------------------------------------

func (a *api) listBudgets(c *gin.Context) {
	userID := auth.PrincipalFrom(c).User.ID
	fr := ingest.Freshness{Complete: true, Warnings: []string{}}
	if earliest, err := a.Budgets.EarliestPeriodStart(c.Request.Context(), userID); err == nil && !earliest.IsZero() {
		fr = a.Ingest.EnsureRange(c.Request.Context(), userID, earliest, a.Ingest.Today(), psu(c))
	}
	list, err := a.Budgets.List(c.Request.Context(), userID)
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"budgets": list, "freshness": fr})
}

func (a *api) createBudget(c *gin.Context) {
	var in budget.Input
	if !a.bindJSON(c, &in) {
		return
	}
	userID := auth.PrincipalFrom(c).User.ID
	b, err := a.Budgets.Create(c.Request.Context(), userID, in)
	if err != nil {
		a.fail(c, err)
		return
	}
	go a.Budgets.EvaluateUser(context.WithoutCancel(c.Request.Context()), userID)
	c.JSON(http.StatusCreated, gin.H{"budget": b})
}

func (a *api) updateBudget(c *gin.Context) {
	var in budget.Input
	if !a.bindJSON(c, &in) {
		return
	}
	userID := auth.PrincipalFrom(c).User.ID
	b, err := a.Budgets.Update(c.Request.Context(), userID, c.Param("id"), in)
	if err != nil {
		a.fail(c, err)
		return
	}
	go a.Budgets.EvaluateUser(context.WithoutCancel(c.Request.Context()), userID)
	c.JSON(http.StatusOK, gin.H{"budget": b})
}

func (a *api) deleteBudget(c *gin.Context) {
	if err := a.Budgets.Delete(c.Request.Context(), auth.PrincipalFrom(c).User.ID, c.Param("id")); err != nil {
		a.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- notifications -----------------------------------------------------------------

func (a *api) listNotifications(c *gin.Context) {
	items, unread, err := a.Notify.List(c.Request.Context(), auth.PrincipalFrom(c).User.ID, queryInt(c, "limit", 50))
	if err != nil {
		a.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"notifications": items, "unread": unread})
}

func (a *api) readNotification(c *gin.Context) {
	if err := a.Notify.MarkRead(c.Request.Context(), auth.PrincipalFrom(c).User.ID, c.Param("id")); err != nil {
		a.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *api) readAllNotifications(c *gin.Context) {
	if err := a.Notify.MarkRead(c.Request.Context(), auth.PrincipalFrom(c).User.ID, ""); err != nil {
		a.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type deviceBody struct {
	Token string `json:"token"`
}

func (a *api) registerDevice(c *gin.Context) {
	var body deviceBody
	if !a.bindJSON(c, &body) {
		return
	}
	if body.Token == "" || len(body.Token) > 4096 {
		badRequest(c, "token is required")
		return
	}
	if err := a.Notify.RegisterDevice(c.Request.Context(), auth.PrincipalFrom(c).User.ID, body.Token, c.GetHeader("User-Agent")); err != nil {
		a.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *api) unregisterDevice(c *gin.Context) {
	var body deviceBody
	if !a.bindJSON(c, &body) {
		return
	}
	if err := a.Notify.UnregisterDevice(c.Request.Context(), auth.PrincipalFrom(c).User.ID, body.Token); err != nil {
		a.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
