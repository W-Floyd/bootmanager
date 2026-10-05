package slots

// The tests build their GPT disk images in memory and need nothing on disk.
// This writes the same images to testdata/ (git-ignored) to look at with
// sgdisk, gdisk or hexdump.
//
//go:generate go run ./gptgen/cmd/gptgen -o testdata
