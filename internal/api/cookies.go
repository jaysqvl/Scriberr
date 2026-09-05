package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func (h *Handler) cookieSecure(c *gin.Context) bool {
	if h == nil || h.config == nil {
		return requestIsSecure(c, nil)
	}

	switch strings.ToLower(strings.TrimSpace(h.config.SecureCookiesMode)) {
	case "true", "force", "secure":
		return true
	case "false", "off", "insecure":
		return false
	default:
		return requestIsSecure(c, h.config.TrustedProxies)
	}
}

func (h *Handler) setAccessTokenCookie(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     "scriberr_access_token",
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(24 * time.Hour),
		HttpOnly: true,
		Secure:   h.cookieSecure(c),
		SameSite: http.SameSiteLaxMode,
	})
}

// syncBrowserAccessCookie repairs sessions restored from the frontend's
// persisted bearer token. Native audio elements cannot attach Authorization
// headers, so they need the matching HttpOnly cookie before mounting.
// AuthMiddleware must run first so only a validated JWT is copied.
func (h *Handler) syncBrowserAccessCookie(c *gin.Context) {
	authType, authenticated := c.Get("auth_type")
	if !authenticated || authType != "jwt" {
		c.Next()
		return
	}

	parts := strings.SplitN(c.GetHeader("Authorization"), " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" {
		c.Next()
		return
	}
	token := parts[1]
	if existing, err := c.Cookie("scriberr_access_token"); err != nil || existing != token {
		h.setAccessTokenCookie(c, token)
	}
	c.Next()
}

// requestIsSecure reports the browser-facing request scheme. Forwarded scheme
// headers are accepted only from an explicitly trusted proxy, matching the
// trust boundary used by Gin for client IP resolution.
func requestIsSecure(c *gin.Context, trustedProxies []string) bool {
	if c != nil && c.Request != nil && c.Request.TLS != nil {
		return true
	}
	if c == nil || c.Request == nil || !remoteIsTrustedProxy(c.Request.RemoteAddr, trustedProxies) {
		return false
	}

	if proto := firstForwardedValue(c.GetHeader("X-Forwarded-Proto")); proto != "" {
		return strings.EqualFold(proto, "https")
	}
	if proto := firstForwardedProto(c.GetHeader("Forwarded")); proto != "" {
		return strings.EqualFold(proto, "https")
	}
	return false
}

func remoteIsTrustedProxy(remoteAddr string, trustedProxies []string) bool {
	if len(trustedProxies) == 0 {
		return false
	}

	host := remoteAddr
	if splitHost, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = splitHost
	}
	remote, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	remote = remote.Unmap()

	trustedRanges := make([]netip.Prefix, 0, len(trustedProxies))
	for _, candidate := range trustedProxies {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(candidate); err == nil {
			trustedRanges = append(trustedRanges, prefix)
			continue
		}
		if address, err := netip.ParseAddr(strings.Trim(candidate, "[]")); err == nil {
			address = address.Unmap()
			trustedRanges = append(trustedRanges, netip.PrefixFrom(address, address.BitLen()))
			continue
		}
		// Gin disables its full trusted-proxy list when any entry is invalid.
		// Match that fail-closed behavior for forwarded scheme headers.
		return false
	}
	for _, prefix := range trustedRanges {
		if prefix.Contains(remote) {
			return true
		}
	}
	return false
}

func firstForwardedValue(value string) string {
	if value == "" {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(value, ",", 2)[0])
}

func firstForwardedProto(value string) string {
	firstHop := firstForwardedValue(value)
	for _, part := range strings.Split(firstHop, ";") {
		key, rawValue, found := strings.Cut(strings.TrimSpace(part), "=")
		if found && strings.EqualFold(strings.TrimSpace(key), "proto") {
			return strings.Trim(strings.TrimSpace(rawValue), "\"")
		}
	}
	return ""
}
