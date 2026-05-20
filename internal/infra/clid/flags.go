package clid

// Flag names exposed by a2textd. Kept in one place so the cli.Flag
// definition and the cmd.String/cmd.Bool read sites cannot drift.
const (
	// FlagConfig overrides the config file path. Defaults to
	// /etc/a2text/system.yaml for system installs and per-user XDG config
	// for user installs.
	FlagConfig = "config"

	// FlagLogLevel overrides config.LogLevel for one invocation.
	FlagLogLevel = "log-level"

	// FlagListenAddr overrides the loopback bind address for the gRPC
	// control plane. Defaults to "127.0.0.1:0" (kernel-assigned port);
	// the chosen port is written to the discovery file so the UI can
	// find it.
	//
	// Bind to 127.0.0.1 only — gRPC traffic uses mTLS but the listener
	// must not be reachable from anything outside the loopback interface.
	FlagListenAddr = "listen"

	// FlagPortFile overrides the file the daemon writes its actual
	// listening port into so the UI can discover it.
	//
	// Default: /run/a2textd/<uid>/port (system install) or
	// $XDG_RUNTIME_DIR/a2textd/port (user install).
	FlagPortFile = "port-file"

	// FlagCertFile and FlagKeyFile point at the daemon's TLS material
	// for the gRPC server. The certificate is pinned by the UI; the key
	// must be mode 0600 and owned by the daemon user.
	FlagCertFile = "cert"
	FlagKeyFile  = "key"

	// FlagClientCAFile points at the PEM bundle of client certificates
	// the daemon will trust for mTLS. Anything not in this bundle is
	// rejected during the TLS handshake.
	FlagClientCAFile = "client-ca"

	// FlagPprof enables the standard net/http/pprof endpoints on the
	// given host:port address (e.g. "127.0.0.1:6060"). Empty / unset =
	// disabled. Bind to loopback only — pprof exposes arbitrary stack
	// and heap inspection to anyone who can connect.
	FlagPprof = "pprof"
)
