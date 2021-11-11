package main

import (
	"bytes"
	"context"
	"log"

	"9fans.net/go/plan9/server"
)

func main() {
	fs, err := server.NewStaticFsys(map[string]server.StaticFile{
		"foo": {
			Content: []byte("bar"),
		},
		"info": {
			Entries: map[string]server.StaticFile{
				"version": {
					Content: []byte("something new"),
				},
				"other": {
					Content: bytes.Repeat([]byte("a"), 1024*1024),
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(server.ServeLocal(context.Background(), "test9p", fs))
}
