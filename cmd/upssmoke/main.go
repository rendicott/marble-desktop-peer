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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if !m.Available() {
		er, err := m.Ensure(ctx, false)
		fmt.Println("ensure", er.Action, er.Port, err)
	}
	url := "https://www.ups.com/track?tracknum=1Z1B0G923548425633"
	fmt.Println("open...")
	if err := m.Open(ctx, url, false); err != nil {
		fmt.Println("open", err)
		os.Exit(1)
	}
	time.Sleep(2500 * time.Millisecond)
	s, err := m.Snapshot(ctx)
	if err != nil {
		fmt.Println("snap", err)
	} else {
		if len(s) > 2800 {
			s = s[:2800] + "…"
		}
		fmt.Println(s)
	}
	fmt.Println("--- click_text Sign Digitally ---")
	res, err := m.Act(ctx, "click_text", "", "Sign Digitally", 0, 0)
	fmt.Println(res, err)
	if err != nil {
		res, err = m.Act(ctx, "click_text", "", "Sign in advance", 0, 0)
		fmt.Println("fallback", res, err)
	}
	time.Sleep(1500 * time.Millisecond)
	s2, _ := m.Snapshot(ctx)
	if len(s2) > 1500 {
		s2 = s2[:1500] + "…"
	}
	fmt.Println("--- after ---")
	fmt.Println(s2)
}
