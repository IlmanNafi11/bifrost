package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseImport_SupportedForms pins the four accepted import-line shapes and the
// resulting Type/URL/Username/Password. Each form uses a distinct host so the dedupe
// step cannot collapse them.
func TestParseImport_SupportedForms(t *testing.T) {
	got, err := ParseImport("10.0.0.1:8080\nhttp://10.0.0.2:8080\nsocks5://user:pass@10.0.0.3:1080\nuser:pass@10.0.0.4:8080")
	require.NoError(t, err)
	require.Len(t, got, 4)

	assert.Equal(t, HTTPProxy, got[0].Type)
	assert.Equal(t, "10.0.0.1:8080", got[0].URL)
	assert.Empty(t, got[0].Username)
	assert.Nil(t, got[0].Password)

	assert.Equal(t, HTTPProxy, got[1].Type)
	assert.Equal(t, "http://10.0.0.2:8080", got[1].URL)
	assert.Empty(t, got[1].Username)
	assert.Nil(t, got[1].Password)

	assert.Equal(t, Socks5Proxy, got[2].Type)
	assert.Equal(t, "socks5://10.0.0.3:1080", got[2].URL)
	assert.Equal(t, "user", got[2].Username)
	require.NotNil(t, got[2].Password)
	assert.Equal(t, "pass", got[2].Password.GetValue())

	assert.Equal(t, HTTPProxy, got[3].Type)
	assert.Equal(t, "10.0.0.4:8080", got[3].URL)
	assert.Equal(t, "user", got[3].Username)
	require.NotNil(t, got[3].Password)
	assert.Equal(t, "pass", got[3].Password.GetValue())
}

// TestParseImport_DedupeAndComments pins host:port dedupe (first form wins, across
// scheme/auth variants) and skipping of blank lines and whole-line # comments.
func TestParseImport_DedupeAndComments(t *testing.T) {
	got, err := ParseImport("# primary\n10.0.0.1:8080\nhttp://10.0.0.1:8080\nsocks5://u:p@10.0.0.1:8080\n\n   \n10.0.0.2:9090\n# trailing comment\n")
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, HTTPProxy, got[0].Type)
	assert.Equal(t, "10.0.0.1:8080", got[0].URL)
	assert.Empty(t, got[0].Username)

	assert.Equal(t, HTTPProxy, got[1].Type)
	assert.Equal(t, "10.0.0.2:9090", got[1].URL)
}

// TestParseImport_RejectsGarbageLine pins that a non-proxy line fails loudly, naming
// the offending line, rather than being silently dropped.
func TestParseImport_RejectsGarbageLine(t *testing.T) {
	_, err := ParseImport("10.0.0.1:8080\nnot a proxy line\n10.0.0.2:8080")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a proxy line")
}

// TestParseImport_EmptyInput pins the zero-value case.
func TestParseImport_EmptyInput(t *testing.T) {
	got, err := ParseImport("")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestProxyMechanismValues pins the wire/DB string values every later task depends on.
func TestProxyMechanismValues(t *testing.T) {
	assert.Equal(t, ProxyMechanism("round_robin"), ProxyRoundRobin)
	assert.Equal(t, ProxyMechanism("random"), ProxyRandom)
	assert.Equal(t, ProxyMechanism("weighted"), ProxyWeighted)
	assert.Equal(t, ProxyMechanism("least_connections"), ProxyLeastConnections)
	assert.Equal(t, ProxyMechanism("fallback"), ProxyFallback)
	assert.Equal(t, ProxyMechanism("sticky_session"), ProxyStickySession)
}
