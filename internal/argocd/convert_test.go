package argocd

import (
	"encoding/json"
	"testing"
)

func TestDecodeAndParseApplications(t *testing.T) {
	raw := []byte(`{
	  "metadata": {},
	  "items": [
	    {
	      "metadata": {"name": "demo", "namespace": "team-a"},
	      "spec": {"project": "default"},
	      "status": {"sync": {"status": "Synced"}, "health": {"status": "Healthy"}}
	    },
	    {
	      "application": {
	        "metadata": {"name": "wrapped", "namespace": "argocd"},
	        "spec": {"project": "ops"}
	      }
	    }
	  ]
	}`)
	apps, err := decodeItemMaps(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 {
		t.Fatalf("got %d apps", len(apps))
	}
	if n := ParseCount(apps); n != 2 {
		t.Fatalf("parsed names=%d sample=%s", n, SampleAppDiag(apps))
	}
	r0 := ResourceFromAppMap(apps[0])
	if r0.Name != "demo" || r0.Namespace != "team-a" || r0.Project != "default" {
		t.Fatalf("app0=%+v", r0)
	}
	r1 := ResourceFromAppMap(apps[1])
	if r1.Name != "wrapped" {
		t.Fatalf("wrapped name=%q", r1.Name)
	}
}

func TestDecodeEmptyItemsOK(t *testing.T) {
	apps, err := decodeItemMaps([]byte(`{"items":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 0 {
		t.Fatalf("got %d", len(apps))
	}
}

func TestResourceFromAppMapJSONRoundTrip(t *testing.T) {
	var m map[string]any
	if err := json.Unmarshal([]byte(`{"metadata":{"name":"x","namespace":"y"},"spec":{"project":"p"}}`), &m); err != nil {
		t.Fatal(err)
	}
	r := ResourceFromAppMap(m)
	if r.Name != "x" || r.Namespace != "y" || r.Project != "p" {
		t.Fatalf("%+v", r)
	}
}
