package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	dir := flag.String("dir", ".", "the package directory to generate for")
	marshal := flag.String("marshal", "", "comma-separated marker interfaces whose implementers get MarshalText")
	roots := flag.String("roots", "", "comma-separated root structs that get UnmarshalText")
	out := flag.String("out", "text_gen.go", "the output file, relative to -dir")
	flag.Parse()

	if *marshal == "" || *roots == "" {
		fmt.Fprintln(os.Stderr, "treegen: -marshal and -roots are required")
		os.Exit(2)
	}
	src, err := Generate(*dir, strings.Split(*marshal, ","), strings.Split(*roots, ","))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*dir+"/"+*out, src, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
