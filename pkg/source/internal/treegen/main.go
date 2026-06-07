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
	custom := flag.String("custom", "", "comma-separated interfaces whose codec is hand-written")
	out := flag.String("out", "text_gen.go", "the output file, relative to -dir")
	flag.Parse()

	if *marshal == "" || *roots == "" {
		fmt.Fprintln(os.Stderr, "treegen: -marshal and -roots are required")
		os.Exit(2)
	}
	cfg := config{
		markers: strings.Split(*marshal, ","),
		roots:   strings.Split(*roots, ","),
		custom:  nameSet(*custom),
	}
	src, err := Generate(*dir, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*dir+"/"+*out, src, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// nameSet splits a comma-separated flag into a set, with "" the empty set.
func nameSet(s string) map[string]bool {
	out := map[string]bool{}
	if s == "" {
		return out
	}
	for _, name := range strings.Split(s, ",") {
		out[name] = true
	}
	return out
}
