// fixture-check exercises acceptance outcomes and container isolation offline.
package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() { os.Exit(run(os.Args[1:])) }
func run(args []string) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "file":
		if len(args) != 3 {
			return 2
		}
		raw, err := os.ReadFile(args[1])
		if err != nil || string(raw) != args[2] {
			fmt.Println("file content mismatch")
			return 1
		}
		fmt.Println("file content matches")
		return 0
	case "fail":
		fmt.Println("intentional failure")
		return 7
	case "sleep":
		if len(args) != 2 {
			return 2
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 || n > 60 {
			return 2
		}
		fmt.Println("sleeping check")
		time.Sleep(time.Duration(n) * time.Second)
		return 0
	case "output":
		if len(args) != 2 {
			return 2
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n > 2<<20 {
			return 2
		}
		_, err = os.Stdout.Write(bytes.Repeat([]byte("x"), n))
		if err != nil {
			return 3
		}
		return 0
	case "isolation":
		success := true
		for _, path := range []string{"/workspace/check-write", "/pack/checks/check-write"} {
			denied := os.WriteFile(path, []byte("forbidden"), 0600) != nil
			fmt.Printf("write denied %s: %t\n", path, denied)
			success = success && denied
		}
		conn, err := net.DialTimeout("tcp", "1.1.1.1:80", 300*time.Millisecond)
		if conn != nil {
			conn.Close()
		}
		fmt.Printf("network denied: %t\n", err != nil)
		success = success && err != nil
		for _, entry := range os.Environ() {
			if strings.HasPrefix(entry, "EASYGO_") {
				success = false
			}
		}
		_, relayErr := os.Stat("/run/easygo-relay/model.sock") // workshop runtimeRelaySocket
		success = success && os.IsNotExist(relayErr)
		fmt.Printf("no credentials or relay: %t\n", success)
		if !success {
			return 1
		}
		return 0
	}
	return 2
}
