package session

import "testing"

func TestContainersFromPodYAML(t *testing.T) {
	pod := `
apiVersion: v1
kind: Pod
metadata:
  name: p
  annotations:
    kubectl.kubernetes.io/default-container: vector
spec:
  containers:
  - name: gatekeeper
    image: gk:1
  - name: vector
    image: vector:0.40
status:
  containerStatuses:
  - name: gatekeeper
    ready: true
    restartCount: 2
    state: {running: {}}
  - name: vector
    ready: false
    state: {waiting: {reason: CrashLoopBackOff}}
`
	cs, err := containersFromPodYAML(pod, "ns", "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || cs[0].Name != "vector" || cs[1].Name != "gatekeeper" {
		t.Fatalf("default-container must come first: %+v", cs)
	}
	if cs[0].State != "CrashLoopBackOff" || cs[0].Ready {
		t.Fatalf("vector state: %+v", cs[0])
	}
	if cs[1].State != "running" || !cs[1].Ready || cs[1].Restarts != 2 {
		t.Fatalf("gatekeeper state: %+v", cs[1])
	}
}
