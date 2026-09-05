package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"scriberr/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCookieSecureMode(t *testing.T) {
	tests := []struct {
		name             string
		mode             string
		remoteAddr       string
		trustedProxies   []string
		tls              bool
		forwardedProto   string
		forwarded        string
		wantSecureCookie bool
	}{
		{name: "auto direct HTTP", mode: "auto", remoteAddr: "192.0.2.10:1234", wantSecureCookie: false},
		{name: "auto direct HTTPS", mode: "auto", remoteAddr: "192.0.2.10:1234", tls: true, wantSecureCookie: true},
		{name: "forced secure on HTTP", mode: "true", remoteAddr: "192.0.2.10:1234", wantSecureCookie: true},
		{name: "forced insecure on HTTPS", mode: "false", remoteAddr: "192.0.2.10:1234", tls: true, wantSecureCookie: false},
		{name: "untrusted forwarded header ignored", mode: "auto", remoteAddr: "192.0.2.10:1234", forwardedProto: "https", wantSecureCookie: false},
		{name: "trusted proxy HTTPS", mode: "auto", remoteAddr: "192.0.2.10:1234", trustedProxies: []string{"192.0.2.10"}, forwardedProto: "https", wantSecureCookie: true},
		{name: "trusted proxy CIDR", mode: "auto", remoteAddr: "192.0.2.10:1234", trustedProxies: []string{"192.0.2.0/24"}, forwarded: `for=203.0.113.7;proto="https"`, wantSecureCookie: true},
		{name: "invalid proxy list fails closed", mode: "auto", remoteAddr: "192.0.2.10:1234", trustedProxies: []string{"192.0.2.10", "not-an-address"}, forwardedProto: "https", wantSecureCookie: false},
		{name: "first forwarded hop wins", mode: "auto", remoteAddr: "192.0.2.10:1234", trustedProxies: []string{"192.0.2.10"}, forwardedProto: "http, https", wantSecureCookie: false},
	}

	gin.SetMode(gin.TestMode)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://scriberr.test/api/v1/auth/login", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.tls {
				req.TLS = &tls.ConnectionState{}
			}
			req.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
			req.Header.Set("Forwarded", tt.forwarded)

			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = req
			handler := &Handler{config: &config.Config{
				SecureCookiesMode: tt.mode,
				TrustedProxies:    tt.trustedProxies,
			}}

			require.Equal(t, tt.wantSecureCookie, handler.cookieSecure(ctx))
		})
	}
}
