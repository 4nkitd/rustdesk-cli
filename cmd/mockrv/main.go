package main

import (
	"fmt"
	"net"
	"os"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/4nkitd/rustdesk-cli/internal/hbb"
	pb "github.com/4nkitd/rustdesk-cli/internal/hbb/proto"
)

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:22116")
	if err != nil {
		fmt.Println("listen:", err)
		os.Exit(1)
	}
	fmt.Println("listening", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		fmt.Fprintln(os.Stderr, "accepted", conn.RemoteAddr())
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			buf := make([]byte, 4096)
			var acc []byte
			for {
				n, err := c.Read(buf)
				if n > 0 {
					acc = append(acc, buf[:n]...)
				}
				if frame, ok := hbb.DecodeFrame(&acc); ok {
					req := &pb.RendezvousMessage{}
					_ = proto.Unmarshal(frame, req)
					peers := req.GetOnlineRequest().GetPeers()
					fmt.Fprintf(os.Stderr, "req peers=%v type=%T\n", peers, req.GetUnion())
					states := make([]byte, (len(peers)+7)/8)
					for i, p := range peers {
						if p == "1" || p == "9" {
							states[i/8] |= 0x01 << (7 - i%8)
						}
					}
					resp := &pb.RendezvousMessage{Union: &pb.RendezvousMessage_OnlineResponse{OnlineResponse: &pb.OnlineResponse{States: states}}}
					data, _ := proto.Marshal(resp)
					fmt.Fprintf(os.Stderr, "resp states=%v type=%T\n", states, resp.GetUnion())
					_, _ = c.Write(hbb.Encode(data))
					return
				}
				if err != nil {
					return
				}
			}
		}(conn)
	}
}
