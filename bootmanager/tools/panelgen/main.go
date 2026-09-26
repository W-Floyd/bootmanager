// Command panelgen writes internal/panel/panels.json: gmobile's
// display-panel files from a pinned release, each reduced by panel.Parse
// and keyed by its DT compatible (the file name).
//
//	go generate ./internal/panel
//
// To move to a new gmobile release, change version and sha256 below. The
// tarball is gmobile's own release, the one distributions build from.
package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"

	"bootmanager.local/bootmanager/internal/panel"
)

const (
	version = "0.7.3"
	sha256x = "d74668e3f5e378748e4dbe0e0338b009c5a9000c407f8782083900ead67377fa"
	url     = "https://sources.phosh.mobi/releases/gmobile/gmobile-" + version + ".tar.xz"
)

func main() {
	out := flag.String("o", "panels.json", "output file")
	src := flag.String("src", "", "a gmobile data/devices/display-panels directory to read instead of downloading")
	flag.Parse()
	log.SetFlags(0)

	var files map[string][]byte
	var err error
	if *src != "" {
		files, err = readDir(*src)
	} else {
		files, err = fetch()
	}
	if err != nil {
		log.Fatal(err)
	}
	if len(files) == 0 {
		log.Fatal("no panels")
	}
	t := map[string]panel.Panel{}
	for name, b := range files {
		p, err := panel.Parse(b)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		t[strings.TrimSuffix(name, ".json")] = p
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("%d panels from gmobile %s -> %s", len(t), version, *out)
}

// fetch downloads the release and returns its display-panel files by name.
func fetch() (map[string][]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != sha256x {
		return nil, fmt.Errorf("%s: sha256 %x, want %s", url, sum, sha256x)
	}
	xr, err := xz.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	dir := "gmobile-" + version + "/data/devices/display-panels"
	files := map[string][]byte{}
	tr := tar.NewReader(xr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if path.Dir(h.Name) == dir && path.Ext(h.Name) == ".json" {
			if files[path.Base(h.Name)], err = io.ReadAll(tr); err != nil {
				return nil, err
			}
		}
	}
}

func readDir(dir string) (map[string][]byte, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, p := range paths {
		if files[filepath.Base(p)], err = os.ReadFile(p); err != nil {
			return nil, err
		}
	}
	return files, nil
}
