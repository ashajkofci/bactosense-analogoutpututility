//go:build !windows && !darwin

package main

import "fmt"

func main() {
	fmt.Println("Analog Output Utility supports Windows and macOS.")
}
