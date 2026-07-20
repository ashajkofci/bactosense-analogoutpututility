//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("Analog Output Utility is a Windows application. Build with GOOS=windows GOARCH=amd64.")
}
