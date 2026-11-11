package middleware

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Hosted requests need a live, unrestricted-by-IP/model platform API key, but
// do not need money in its platform wallet. Paid fallback runs TokenAuth again.
func HostedTokenAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			hostedAuthError(c)
			return
		}
		key := strings.TrimPrefix(parts[1], "sk-")
		if len(key) == 0 || len(key) > 128 || strings.Contains(key, "-") {
			hostedAuthError(c)
			return
		}
		var token model.Token
		if model.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Where(map[string]any{"key": key}).First(&token).Error != nil {
			hostedAuthError(c)
			return
		}
		if (token.Status != common.TokenStatusEnabled && token.Status != common.TokenStatusExhausted) || (token.ExpiredTime != -1 && token.ExpiredTime <= time.Now().Unix()) {
			hostedAuthError(c)
			return
		}
		if allowed := token.GetIpLimits(); len(allowed) > 0 {
			ip := net.ParseIP(c.ClientIP())
			if ip == nil || !common.IsIpInCIDRList(ip, allowed) {
				hostedAuthError(c)
				return
			}
		}
		user, err := model.GetUserById(token.UserId, false)
		if err != nil || user.Status != common.UserStatusEnabled {
			hostedAuthError(c)
			return
		}
		c.Set("id", user.Id)
		c.Set("hosted_token", &token)
		c.Next()
	}
}

func hostedAuthError(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": i18n.T(c, i18n.MsgTokenInvalid), "type": "authentication_error", "code": "hosted_auth_failed"}})
}
