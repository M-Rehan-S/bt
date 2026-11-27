package main

import (
	"fmt"
	"os"

	"github.com/M-Rehan-S/bt/internal/metainfo"
)

func main() {
	t, err := metainfo.Load(os.Args[1])
	if err != nil {
		panic(err)
	}
	fmt.Printf("%x\n%d pieces\n", t.InfoHash, len(t.PieceHashes))
}
