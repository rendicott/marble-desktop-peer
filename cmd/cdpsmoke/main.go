package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/rendicott/marble-desktop-peer/internal/browser"
	"github.com/rendicott/marble-desktop-peer/internal/config"
)

func main() {
	_ = os.Setenv("MARBLE_PEER_HOME", config.Home())
	m := browser.New()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		fmt.Println("start:", err)
	}
	fmt.Println("available", m.Available())
	url := "https://weather.com/weather/today/l/Dunwoody+GA"
	if err := m.Open(ctx, url, false); err != nil {
		fmt.Println("open:", err)
		return
	}
	time.Sleep(2 * time.Second)
	s, err := m.Snapshot(ctx)
	if err != nil {
		fmt.Println("snap err", err)
		return
	}
	if len(s) > 2000 {
		s = s[:2000] + "\n…"
	}
	fmt.Println(s)
}
