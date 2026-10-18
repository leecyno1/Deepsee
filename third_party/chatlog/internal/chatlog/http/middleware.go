package http

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/sjzar/chatlog/internal/chatlog/ports"
)

const databaseRequestContextKey = "chatlog.database_request"

func hostOnly(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" || strings.HasPrefix(hostport, ":") {
		return ""
	}
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.Trim(strings.ToLower(host), "[]")
	}
	return strings.Trim(strings.ToLower(hostport), "[]")
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isWildcardHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	return host == "" || host == "0.0.0.0" || host == "::"
}

// ValidateListenAddress deliberately confines the control plane to the local
// machine. Chat history and raw database access must not be reachable from a
// LAN interface without a separate authenticated gateway.
func ValidateListenAddress(addr string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || strings.TrimSpace(port) == "" {
		return fmt.Errorf("invalid HTTP listen address %q", addr)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("HTTP listen address must use localhost or a loopback IP")
	}
	return nil
}

func requestBodyLimitMiddleware() gin.HandlerFunc {
	const defaultLimit = int64(2 << 20)
	return func(c *gin.Context) {
		if c.Request.Body != nil && c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, defaultLimit)
		}
		c.Next()
	}
}

func requestHostAllowed(boundHost, requestHost string) bool {
	if isWildcardHost(boundHost) {
		return true
	}
	if isLoopbackHost(boundHost) {
		return isLoopbackHost(requestHost)
	}
	return isLoopbackHost(requestHost) || strings.EqualFold(boundHost, requestHost)
}

func originAllowed(boundHost, requestAuthority, origin string) bool {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	if !strings.EqualFold(u.Host, strings.TrimSpace(requestAuthority)) {
		return false
	}
	return !isLoopbackHost(boundHost) || isLoopbackHost(u.Hostname())
}

func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data: blob:; object-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Next()
	}
}

func corsMiddleware(httpAddr string) gin.HandlerFunc {
	boundHost := hostOnly(httpAddr)
	return func(c *gin.Context) {
		requestHost := hostOnly(c.Request.Host)
		if !requestHostAllowed(boundHost, requestHost) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		if origin := c.Request.Header.Get("Origin"); origin != "" {
			if !originAllowed(boundHost, c.Request.Host, origin) {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			c.Writer.Header().Add("Vary", "Origin")
		}
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-CSRF-Token, X-Chatlog-State")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "Content-Disposition, X-Chatlog-Export-Mode, X-Chatlog-Has-More, X-Chatlog-Next-Offset, X-Chatlog-Row-Limit, X-Chatlog-Row-Offset")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

func (s *Service) checkDBStateMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		state, stateMessage := s.db.Status()
		switch state {
		case ports.DatabaseStateInit:
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database is not ready"})
			c.Abort()
			return
		case ports.DatabaseStateOpening:
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database direct reader is opening, please wait"})
			c.Abort()
			return
		case ports.DatabaseStateError:
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "database is error: " + stateMessage})
			c.Abort()
			return
		}

		c.Set(databaseRequestContextKey, true)
		c.Next()
	}
}
