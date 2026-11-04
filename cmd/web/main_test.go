package main

import (
	"flag"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInitFlags covers the --bind handling specifically: a wrong value here is not a
// crash but a silently wider exposure, so every rejected form is pinned.
//
//nolint:paralleltest // initFlags writes the package-level HttpBindAddress and HttpPort, and the subtests swap the flag pointers it reads from.
func TestInitFlags(t *testing.T) {
	origBind, origPort := HttpBindAddress, HttpPort
	origFlagBind := flagBindAddress
	t.Cleanup(func() {
		HttpBindAddress, HttpPort = origBind, origPort
		flagBindAddress = origFlagBind
	})

	// applyBind runs initFlags with --bind set to value and returns the resulting
	// HttpBindAddress, always starting from the loopback default so a subtest never
	// inherits the previous one's override.
	applyBind := func(t *testing.T, value string) string {
		t.Helper()
		HttpBindAddress = "127.0.0.1"
		flagBindAddress = &value
		initFlags()
		return HttpBindAddress
	}

	t.Run("an explicit IPv4 address overrides the default", func(t *testing.T) {
		require.Equal(t, "0.0.0.0", applyBind(t, "0.0.0.0"), "a container deployment must be able to publish on every interface")
	})

	t.Run("an IPv6 literal is accepted", func(t *testing.T) {
		require.Equal(t, "::1", applyBind(t, "::1"))
		require.Equal(t, "::", applyBind(t, "::"))
	})

	t.Run("surrounding whitespace is trimmed", func(t *testing.T) {
		require.Equal(t, "0.0.0.0", applyBind(t, "  0.0.0.0  "))
	})

	t.Run("an empty value leaves the loopback default alone", func(t *testing.T) {
		require.Equal(t, "127.0.0.1", applyBind(t, ""))
	})

	// Every form below would otherwise be joined with HttpPort into an address the
	// kernel cannot bind, or resolved to whichever address net.Listen picked for us.
	// All of them must fall back to loopback rather than to the wildcard.
	t.Run("a value carrying a port is rejected", func(t *testing.T) {
		for _, value := range []string{":8000", "127.0.0.1:8000", "[::1]:8000", "0.0.0.0:8000"} {
			require.Equalf(t, "127.0.0.1", applyBind(t, value), "--bind %q must fall back to the loopback default", value)
		}
	})

	t.Run("a hostname is rejected", func(t *testing.T) {
		for _, value := range []string{"localhost", "beacon.seilbekskindirov.dev", "[::1]", "not-an-ip"} {
			require.Equalf(t, "127.0.0.1", applyBind(t, value), "--bind %q must fall back to the loopback default", value)
		}
	})
}

// TestListenAddress pins the address main() hands to net.Listen. The IPv6 cases are
// the reason it goes through net.JoinHostPort: "::1" concatenated with a port is
// unbindable, bracketed it is fine.
func TestListenAddress(t *testing.T) {
	t.Parallel()

	t.Run("joins host and port", func(t *testing.T) {
		t.Parallel()
		for _, c := range []struct {
			host string
			port int
			want string
		}{
			{host: "127.0.0.1", port: 8080, want: "127.0.0.1:8080"},
			{host: "127.0.0.1", port: 8000, want: "127.0.0.1:8000"},
			{host: "0.0.0.0", port: 8000, want: "0.0.0.0:8000"},
			{host: "::1", port: 8000, want: "[::1]:8000"},
			{host: "::", port: 8000, want: "[::]:8000"},
		} {
			require.Equalf(t, c.want, listenAddress(c.host, c.port), "host %q port %d", c.host, c.port)
		}
	})
}

// TestBindDefaultIsLoopback is the regression guard for issue #93: cmd/web bound
// *:8000 while the nginx vhost documented the path as loopback, so an unauthenticated
// /health/check — deployed version, uptime, per-dependency status — was published on
// every interface the host had, with only the firewall in the way. Nothing failed and
// no log line said so; only ss -ltn did.
//
// It reads the flag's registered default rather than HttpBindAddress because the
// global is mutable and TestInitFlags moves it; DefValue is fixed at init.
func TestBindDefaultIsLoopback(t *testing.T) {
	t.Parallel()

	f := flag.Lookup("bind")
	require.NotNil(t, f, "flag \"bind\" must be registered in init")
	require.Equal(t, "127.0.0.1", f.DefValue)

	ip := net.ParseIP(f.DefValue)
	require.NotNilf(t, ip, "the --bind default must be an IP literal, initFlags rejects anything else: %q", f.DefValue)
	require.Truef(t, ip.IsLoopback(), "the --bind default must be a loopback address, got %s", ip)

	// The assertions above only inspect a string. Binding it is what proves the kernel
	// agrees. Port 0 asks for an ephemeral port, so this never collides with a port
	// already in use on the machine running the tests.
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", listenAddress(f.DefValue, 0))
	require.NoError(t, err)
	t.Cleanup(func() {
		if e := listener.Close(); e != nil {
			t.Logf("close listener: %v", e)
		}
	})

	addr, ok := listener.Addr().(*net.TCPAddr)
	require.Truef(t, ok, "expected a *net.TCPAddr, got %T", listener.Addr())
	require.Truef(t, addr.IP.IsLoopback(), "cmd/web must not bind %s by default", addr.IP)
	require.Falsef(t, addr.IP.IsUnspecified(), "cmd/web must not bind the wildcard address %s by default", addr.IP)
}

// TestFlagsAreRegistered pins that init registers the flags rather than parsing them,
// so the binary still accepts them on the command line.
func TestFlagsAreRegistered(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"port", "bind", "timeout", "logs-dir", "verbosity", "static-dir", "api-dsn"} {
		require.NotNilf(t, flag.Lookup(name), "flag %q must be registered in init", name)
	}
}
