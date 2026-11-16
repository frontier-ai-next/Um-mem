package localservice

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"time"
)

// StartListener binds only to loopback, publishes the Python-compatible
// registry after the socket is live, and removes it when serving stops.
// Readiness remains controlled by the Server health contract.
func (server *Server) StartListener(ctx context.Context, ownerPID int) (string, <-chan error, error) {
	if server == nil {
		return "", nil, errors.New("local service server is nil")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	paths, err := writeRegistry(server.identity, port, ownerPID)
	if err != nil {
		listener.Close()
		return "", nil, err
	}
	server.mu.Lock()
	server.listenerPort = port
	server.mu.Unlock()
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes: MaxMessageBytes}
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-server.shutdown:
		case <-stopped:
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	go func() {
		serveErr := httpServer.Serve(listener)
		close(stopped)
		server.mu.Lock()
		server.backgroundReady = false
		server.listenerPort = 0
		server.mu.Unlock()
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		clearErr := clearRegistry(paths, os.Getpid(), port)
		done <- errors.Join(serveErr, clearErr)
		close(done)
	}()
	return listener.Addr().String(), done, nil
}
