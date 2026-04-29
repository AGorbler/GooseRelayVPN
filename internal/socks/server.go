package socks

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/kianmhz/GooseRelayVPN/internal/session"
	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

// SessionFactory creates a new tunneled session for the given "host:port"
// target. The returned session is owned by the carrier (which polls it for
// outgoing frames and routes incoming ones).
type SessionFactory func(target string) *session.Session

// Serve starts a SOCKS5 listener on listenAddr that wraps every connection in
// a VirtualConn over a fresh tunneled session. The DNS resolver is overridden
// with a no-op to prevent local DNS leaks (clients must use socks5h://).
//
// When user and pass are both non-empty, RFC 1929 username/password
// authentication is required; unauthenticated clients are rejected.
//
// Blocks until ListenAndServe returns. Caller passes ctx for shutdown
// signaling (the underlying go-socks5 library doesn't take a ctx, so this
// just wires it through for parity with the rest of the codebase).
func Serve(_ context.Context, listenAddr, user, pass string, factory SessionFactory) error {
	opts := []socks5.Option{
		socks5.WithDial(func(_ context.Context, _, addr string) (net.Conn, error) {
			s := factory(addr)
			log.Printf("[socks] new session %x for %s", s.ID[:4], addr)
			return NewVirtualConn(s), nil
		}),
		socks5.WithAssociateHandle(func(_ context.Context, w io.Writer, _ *socks5.Request) error {
			_ = socks5.SendReply(w, statute.RepCommandNotSupported, nil)
			return fmt.Errorf("UDP associate not supported")
		}),
		socks5.WithResolver(noopResolver{}),
	}
	if user != "" {
		opts = append(opts, socks5.WithAuthMethods([]socks5.Authenticator{
			socks5.UserPassAuthenticator{
				Credentials: socks5.StaticCredentials{user: pass},
			},
		}))
	}
	server := socks5.NewServer(opts...)
	return server.ListenAndServe("tcp", listenAddr)
}

// noopResolver is a SOCKS5 name resolver that returns the host string verbatim
// (no DNS lookup). Combined with socks5h:// clients, this keeps DNS off the
// local machine entirely — it's resolved on the VPS exit instead.
type noopResolver struct{}

func (noopResolver) Resolve(ctx context.Context, _ string) (context.Context, net.IP, error) {
	return ctx, nil, nil
}
