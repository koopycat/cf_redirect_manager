package csvio

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadStrictCSV(t *testing.T) {
	items, err := Read(strings.NewReader("source,target\nhttps://a.example/x,https://b.example/y\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].StatusCode != 301 {
		t.Fatalf("unexpected items: %#v", items)
	}

	bad := []string{
		"target,source\nhttps://a.example,https://b.example\n",
		"source,target,comment\na,b,c\n",
		"source,target\nhttps://a.example,https://b.example,extra\n",
		"source,target\n/a,https://b.example\n",
		"source,target\nhttps://a.example,https://b.example\nhttps://a.example,https://c.example\n",
	}
	for _, input := range bad {
		if _, err := Read(strings.NewReader(input)); err == nil {
			t.Errorf("Read(%q) unexpectedly succeeded", input)
		}
	}
}

func TestWriteAndReadQuotedValues(t *testing.T) {
	input := "source,target\n\"https://a.example/x,y\",https://b.example/y\n"
	items, err := Read(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Write(&output, items); err != nil {
		t.Fatal(err)
	}
	again, err := Read(&output)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Source != "https://a.example/x,y" {
		t.Fatalf("unexpected round trip: %#v", again)
	}
}
