package server

import (
	"context"
	"fmt"
	"log"
	"net"

	"9fans.net/go/plan9/client"
)

func ServeNet[F Fid](ctx context.Context, proto, addr string, fs Fsys[F]) error {
	lis, err := net.Listen(proto, addr)
	if err != nil {
		return err
	}
	for {
		conn, err := lis.Accept()
		if err != nil {
			return fmt.Errorf("accept failed: %v", err)
		}
		go func() {
			err := Serve(ctx, conn, fs)
			if err != nil {
				log.Printf("serve error on %v: %v", conn.RemoteAddr(), err)
			}
		}()
	}
}

func ServeLocal[F Fid](ctx context.Context, name string, fs Fsys[F]) error {
	if name == "" {
		return fmt.Errorf("9p server name is empty")
	}
	ns := client.Namespace()
	return ServeNet(ctx, "unix", ns+"/"+name, fs)
}
