// Package httpapi exposes PFF's REST API (Gin). Handlers stay thin: they parse
// and validate input, call a service, and render JSON. All business rules live
// in the service packages.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cecinuga/pff/ms/internal/analytics"
	"github.com/cecinuga/pff/ms/internal/auth"
	"github.com/cecinuga/pff/ms/internal/budget"
	"github.com/cecinuga/pff/ms/internal/config"
	"github.com/cecinuga/pff/ms/internal/connect"
	"github.com/cecinuga/pff/ms/internal/ingest"
	"github.com/cecinuga/pff/ms/internal/notify"
	"github.com/cecinuga/pff/ms/internal/platform/httperr"
	"github.com/cecinuga/pff/ms/internal/warehouse"
)

// Deps are the services the API needs.
type Deps struct {
	Cfg       config.Config
	Log       *slog.Logger
	Pool      *pgxpool.Pool
	Auth      *auth.Authenticator
	OTP       *auth.EmailOTP
	Ingest    *ingest.Service
	Connect   *connect.Service
	Analytics *analytics.Service
	Warehouse *warehouse.Store
	Budgets   *budget.Service
	Notify    *notify.Service
}

type api struct{ Deps }

const maxBodyBytes = 1 << 20

// NewRouter builds the Gin engine with every route.
func NewRouter(d Deps) (*gin.Engine, error) {
	if !d.Cfg.IsDev() {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	if err := r.SetTrustedProxies(d.Cfg.TrustedProxies); err != nil {
		return nil, err
	}
	a := &api{d}
	r.Use(a.recovery(), a.requestLog(), securityHeaders(), limitBody())
	r.NoRoute(func(c *gin.Context) { httperr.Abort(c, http.StatusNotFound, "not_found", "no such endpoint") })

	r.GET("/healthz", a.health)

	v1 := r.Group("/api/v1")
	v1.GET("/public/config", a.publicConfig)

	authed := v1.Group("", d.Auth.Middleware())
	authed.GET("/me", a.me)
	authed.POST("/auth/mfa/email/challenge", a.emailChallenge)
	authed.POST("/auth/mfa/email/verify", a.emailVerify)
	authed.DELETE("/auth/mfa/email", a.emailDisable)

	data := authed.Group("", auth.RequireMFA())
	data.GET("/meta", a.meta)

	data.GET("/banks", a.banks)
	data.GET("/connections", a.listConnections)
	data.POST("/connections", a.startConnection)
	data.POST("/connections/callback", a.completeConnection)
	data.DELETE("/connections/:id", a.revokeConnection)

	data.GET("/metrics", a.metrics)
	data.GET("/allocation", a.allocation)
	data.GET("/history", a.history)
	data.GET("/history/series", a.series)
	data.GET("/transactions/:id", a.transaction)
	data.PUT("/transactions/:id/category", a.setCategory)

	data.GET("/budgets", a.listBudgets)
	data.POST("/budgets", a.createBudget)
	data.PUT("/budgets/:id", a.updateBudget)
	data.DELETE("/budgets/:id", a.deleteBudget)

	data.GET("/notifications", a.listNotifications)
	data.POST("/notifications/read-all", a.readAllNotifications)
	data.POST("/notifications/:id/read", a.readNotification)
	data.POST("/push/devices", a.registerDevice)
	data.DELETE("/push/devices", a.unregisterDevice)

	return r, nil
}

func (a *api) health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := a.Pool.Ping(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ---- middleware -------------------------------------------------------------

func (a *api) recovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, err any) {
		a.Log.Error("panic", "err", err, "path", c.FullPath())
		httperr.Abort(c, http.StatusInternalServerError, "internal", "internal error")
	})
}

// requestLog logs method, route and status. Query strings are not logged: they
// may carry dates of personal data and must never carry secrets.
func (a *api) requestLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		lvl := slog.LevelInfo
		if c.Writer.Status() >= 500 {
			lvl = slog.LevelError
		}
		a.Log.Log(c.Request.Context(), lvl, "http", "method", c.Request.Method, "route", route,
			"status", c.Writer.Status(), "took", time.Since(start).Round(time.Millisecond))
	}
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		c.Next()
	}
}

func limitBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
		}
		c.Next()
	}
}
