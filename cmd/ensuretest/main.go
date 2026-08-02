package main
import (
  "context"
  "encoding/json"
  "fmt"
  "os"
  "time"
  "github.com/rendicott/marble-desktop-peer/internal/browser"
)
func main() {
  os.Setenv("DISPLAY", ":0")
  m := browser.NewWithOptions(browser.Options{Mode: "user"})
  ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
  defer cancel()
  er, err := m.Ensure(ctx, true)
  b,_ := json.MarshalIndent(er, "", "  ")
  fmt.Println(string(b))
  if err != nil { fmt.Println("ERR", err); os.Exit(1) }
  s, err := m.Snapshot(ctx)
  if err != nil { fmt.Println("snap", err); os.Exit(1) }
  if len(s) > 800 { s = s[:800]+"…" }
  fmt.Println(s)
  // leave browser running for user
}
