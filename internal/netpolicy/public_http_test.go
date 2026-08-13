package netpolicy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticResolver []netip.Addr

func (r staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r, nil
}

func TestValidatePublicURL(t *testing.T) {
	for _, destination := range []string{
		"https://127.0.0.1/v1",
		"https://10.0.0.1/v1",
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/v1",
		"https://100.64.0.1/v1",
		"https://198.18.0.1/v1",
		"https://192.0.2.1/v1",
		"https://[2001:db8::1]/v1",
		"https://localhost/v1",
		"http://example.com/v1",
		"https://user:pass@example.com/v1",
	} {
		t.Run(destination, func(t *testing.T) {
			_, err := ValidatePublicURL(destination, true)
			assert.Error(t, err)
		})
	}

	parsed, err := ValidatePublicURL("https://api.openai.com/v1", true)
	require.NoError(t, err)
	assert.Equal(t, "api.openai.com", parsed.Hostname())
}

func TestPublicDialRejectsAnyProtectedDNSAnswer(t *testing.T) {
	dial := publicDialContext(&net.Dialer{Timeout: time.Second}, staticResolver{
		netip.MustParseAddr("93.184.216.34"),
		netip.MustParseAddr("127.0.0.1"),
	})

	connection, err := dial(context.Background(), "tcp", "example.test:443")
	if connection != nil {
		_ = connection.Close()
	}
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPrivateAddress))
}

func TestIsPublicAddress(t *testing.T) {
	assert.True(t, isPublicAddress(netip.MustParseAddr("8.8.8.8")))
	assert.True(t, isPublicAddress(netip.MustParseAddr("2606:4700:4700::1111")))
	assert.False(t, isPublicAddress(netip.MustParseAddr("100.64.0.1")))
	assert.False(t, isPublicAddress(netip.MustParseAddr("198.18.0.1")))
	assert.False(t, isPublicAddress(netip.MustParseAddr("2001:db8::1")))
	assert.False(t, isPublicAddress(netip.MustParseAddr("192.168.1.10")))
}

func TestPublicHTTPClientRejectsProtectedRedirect(t *testing.T) {
	client := NewPublicHTTPClient(time.Second)
	req, err := http.NewRequest(http.MethodGet, "https://127.0.0.1/internal", nil)
	require.NoError(t, err)
	err = client.CheckRedirect(req, []*http.Request{{URL: mustParseURL(t, "https://example.com")}})
	assert.ErrorIs(t, err, ErrPrivateAddress)
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	require.NoError(t, err)
	return parsed
}
