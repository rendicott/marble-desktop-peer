package app

import (
	"encoding/json"
	"testing"
)

func payload(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCaptureOptsParsing(t *testing.T) {
	o, err := captureOpts(payload(t, `{}`))
	if err != nil || o.Region != nil || o.MaxEdge != 0 || o.Scale != 0 {
		t.Fatalf("empty payload must be today's full shot: %+v %v", o, err)
	}
	if o, _ := captureOpts(payload(t, `{"max_edge":0}`)); o.MaxEdge != -1 {
		t.Fatalf("max_edge 0 means uncapped: %+v", o)
	}
	if o, _ := captureOpts(payload(t, `{"max_edge":2000,"scale":1}`)); o.MaxEdge != 2000 || o.Scale != 1 {
		t.Fatalf("%+v", o)
	}
	o, err = captureOpts(payload(t, `{"region":{"x":640,"y":200,"w":400,"h":500},"space":"screen"}`))
	if err != nil || o.Region == nil || o.Region.X != 640 || o.Region.W != 400 {
		t.Fatalf("screen region: %+v %v", o, err)
	}
	if _, err := captureOpts(payload(t, `{"region":{"x":0,"y":0,"w":10,"h":10},"space":"bogus"}`)); err == nil {
		t.Fatal("unknown space should error")
	}
}
