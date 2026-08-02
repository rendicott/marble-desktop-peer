package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/rendicott/marble-desktop-peer/internal/browser"
)

func main() {
	m := browser.NewWithOptions(browser.Options{Mode: "user", CDPPort: 9222})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if !m.Available() {
		er, err := m.Ensure(ctx, true)
		fmt.Println("ensure", er.Action, er.Port, err)
		if err != nil {
			os.Exit(1)
		}
	}
	q := "560049223"
	if len(os.Args) > 1 {
		q = os.Args[1]
	}
	fmt.Println("open_gmail", q, "...")
	res, err := m.Act(ctx, "open_gmail", "", q, 0, 0)
	if err != nil {
		fmt.Println("open_gmail err:", err)
		os.Exit(1)
	}
	fmt.Println(res)
	time.Sleep(800 * time.Millisecond)
	s, err := m.Snapshot(ctx)
	if err != nil {
		fmt.Println("snap", err)
		os.Exit(1)
	}
	if len(s) > 6000 {
		s = s[:6000] + "…"
	}
	fmt.Println("--- snapshot ---")
	fmt.Println(s)
}
