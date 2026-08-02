package main
import (
  "context"
  "fmt"
  "os"
  "time"
  "github.com/rendicott/marble-desktop-peer/internal/desktop"
)
func main() {
  ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
  defer cancel()
  ok, note := desktop.Available()
  fmt.Println("available", ok, note)
  if err := desktop.ProbeClick(ctx); err != nil {
    fmt.Println("probe", err)
  } else {
    fmt.Println("probe ok")
  }
  img, meta, err := desktop.Screenshot(ctx)
  if err != nil {
    fmt.Println("shot", err)
    os.Exit(1)
  }
  fmt.Printf("screenshot bytes=%d w=%d h=%d scale=%v\n", len(img), meta.W, meta.H, meta.Scale)
  // click center-ish of screen
  x, y := meta.W/2, meta.H/2
  if x < 10 { x = 400 }
  if y < 10 { y = 300 }
  if err := desktop.Click(ctx, x, y, "1"); err != nil {
    fmt.Println("click", err)
    os.Exit(1)
  }
  fmt.Println("click ok at", x, y)
  if err := desktop.Key(ctx, "Escape"); err != nil {
    fmt.Println("key", err)
  } else {
    fmt.Println("key Escape ok")
  }
}
