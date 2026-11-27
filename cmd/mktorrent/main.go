package main

import (
	"crypto/sha1"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/M-Rehan-S/bt/internal/bencode"
)

const pieceLen = 262144 // 256 KB, fixed by the proposal

func main() {
	file := flag.String("file", "", "input file")
	announce := flag.String("announce", "", "tracker announce URL")
	out := flag.String("out", "out.torrent", "output .torrent path")
	flag.Parse()
	if *file == "" || *announce == "" {
		log.Fatal("usage: mktorrent --file F --announce URL --out O")
	}

	f, err := os.Open(*file)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()

	var pieces []byte
	buf := make([]byte, pieceLen)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			h := sha1.Sum(buf[:n])
			pieces = append(pieces, h[:]...)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
	}

	info := map[string]interface{}{
		"name":         filepath.Base(*file),
		"length":       st.Size(),
		"piece length": int64(pieceLen),
		"pieces":       string(pieces),
	}
	meta := map[string]interface{}{"announce": *announce, "info": info}
	data, err := bencode.Encode(meta)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, data, 0644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s: %d pieces\n", *out, len(pieces)/20)
}