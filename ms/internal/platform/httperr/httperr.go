// Package httperr defines PFF's JSON error envelope:
//
//	{"error": {"code": "not_whitelisted", "message": "..."}}
//
// Codes are stable identifiers the client can branch on; messages are for humans.
package httperr

import (
	"github.com/gin-gonic/gin"
)

// Body is the error envelope.
type Body struct {
	Error Detail `json:"error"`
}

// Detail describes one error.
type Detail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Abort writes the envelope and stops the handler chain.
func Abort(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, Body{Error: Detail{Code: code, Message: message}})
}
