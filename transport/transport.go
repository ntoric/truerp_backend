package transport

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	ModeIPC  = "ipc"
	ModeREST = "rest"

	DefaultRESTAddr = ":8088"
)

// Mode returns the API transport from API_TRANSPORT (default: rest).
func Mode() string {
	m := strings.ToLower(strings.TrimSpace(os.Getenv("API_TRANSPORT")))
	switch m {
	case ModeIPC, "unix", "socket":
		return ModeIPC
	case ModeREST, "http", "tcp", "":
		return ModeREST
	default:
		return m
	}
}

// RESTAddr returns the TCP listen address for REST mode.
// Prefers API_ADDR, then PORT (common on cloud hosts), then :8088.
func RESTAddr() string {
	if addr := strings.TrimSpace(os.Getenv("API_ADDR")); addr != "" {
		return addr
	}
	if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
		if strings.HasPrefix(port, ":") {
			return port
		}
		return ":" + port
	}
	return DefaultRESTAddr
}

// SocketPath returns the unix-domain socket path for IPC mode.
func SocketPath() string {
	if p := strings.TrimSpace(os.Getenv("API_SOCKET_PATH")); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), "truerp-api.sock")
}

// ListenIPC creates a unix-domain socket listener, removing any stale socket first.
func ListenIPC(socketPath string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", socketPath, err)
	}

	// Restrict socket access to the current user where the OS supports chmod on sockets.
	if runtime.GOOS != "windows" {
		_ = os.Chmod(socketPath, 0o600)
	}
	return ln, nil
}
