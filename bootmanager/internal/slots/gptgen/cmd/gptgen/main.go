// Command gptgen writes the test fixtures' GPT disk images to a directory,
// as full-size sparse files, for inspection with sgdisk, gdisk or hexdump:
//
//	go run ./gptgen/cmd/gptgen -o testdata
//
// The tests do not need it: they build the same images in memory.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"bootmanager.local/bootmanager/internal/slots/gptgen"
)

func main() {
	out := flag.String("o", "testdata", "directory to write the images to")
	bs := flag.Int64("bs", 4096, "logical block size (4096 as UFS, or 512)")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "gptgen:", err)
		os.Exit(1)
	}
	ims, err := gptgen.FogonaImages(*bs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gptgen:", err)
		os.Exit(1)
	}
	for _, im := range ims {
		p := filepath.Join(*out, fmt.Sprintf("%s-bs%d.img", im.Name, im.BlockSize))
		if err := im.WriteFile(p); err != nil {
			fmt.Fprintln(os.Stderr, "gptgen:", err)
			os.Exit(1)
		}
		fmt.Println(p)
	}
}
